package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"workflow-api/internal/dto"
	"workflow-api/internal/matchingcontract"
	"workflow-api/internal/model"
)

func TestAssignmentProducersMatchApprovedContracts(t *testing.T) {
	for _, scenario := range []string{"initial_assignment", "replacement_assignment", "collection_completed", "collection_failed"} {
		t.Run(scenario, func(t *testing.T) {
			repo := newFakeAssignmentRepository()
			repo.batch.ID = "b4000000-0000-4000-8000-000000000001"
			repo.claim.ID = "c4000000-0000-4000-8000-000000000001"
			repo.claim.BatchID = repo.batch.ID
			repo.batch.CurrentClaimID = new(repo.claim.ID)
			scopeID := "d4000000-0000-4000-8000-000000000001"
			repo.scope.ID = scopeID
			assignedAt := time.Date(2026, time.January, 1, 10, 0, 0, 123456789, time.UTC)
			service := NewAssignmentWorkflowService(repo)
			service.clock = func() time.Time { return assignedAt }
			nextID := 0
			service.newID = func() string {
				nextID++
				return fmt.Sprintf("e4000000-0000-4000-8000-%012d", nextID)
			}
			var previousID any
			sequence := "1"
			if scenario == "replacement_assignment" {
				previousID = "a4000000-0000-4000-8000-000000000001"
				sequence = "8"
				repo.assignment = &model.BatchAssignment{
					ID: previousID.(string), BatchID: repo.batch.ID, ClaimID: repo.claim.ID,
					CollectorUserID: "USR-PREVIOUS", CollectorOrgID: "COL-001",
					AssignmentStatus: model.AssignmentStatusFailed, AssignmentSequence: 7,
				}
			}
			selectAssignment := func() (dto.AssignmentMutationResult, error) {
				return service.Select(context.Background(), repo.batch.ID,
					dto.AssignmentSelectionRequest{ExpectedVersion: 1, ClaimEpoch: "1", CollectorScopeID: scopeID},
					testAssignmentMetadata("selection-event-contract"))
			}
			selection, err := selectAssignment()
			if err != nil {
				t.Fatal(err)
			}
			if len(repo.outbox) != 1 {
				t.Fatal("selection must enqueue exactly one event")
			}
			data := validateC4ProducerEvent(t, repo.outbox[0], model.CollectorAssignedEventType, "batch.collector.assigned")
			for field, expected := range map[string]any{
				"recycler_org_id": "REC-001", "assignment_version": "1",
				"previous_assignment_id": previousID, "assignment_sequence": sequence,
			} {
				if value, present := data[field]; !present || value != expected {
					t.Errorf("%s: got %#v (present=%t), want %#v", field, value, present, expected)
				}
			}
			invoke, first := selectAssignment, selection
			if strings.HasPrefix(scenario, "collection_") {
				// Preserve the original assignment's scope even if live scope data changes.
				repo.scope.ID = "d4000000-0000-4000-8000-000000000002"
				service.clock = func() time.Time { return assignedAt.Add(time.Hour) }
				metadata := testAssignmentMetadata("collection-event-contract")
				metadata.ExpectedVersion = 2
				failed := scenario == "collection_failed"
				invoke = func() (dto.AssignmentMutationResult, error) {
					if failed {
						return service.Fail(context.Background(), selection.Data.AssignmentID,
							dto.FailedPickupRequest{FailureReason: "DONOR_UNAVAILABLE"}, metadata)
					}
					return service.Handoff(context.Background(), selection.Data.AssignmentID, dto.HandoffRequest{
						PickupOccurredAt: assignedAt.Add(30 * time.Minute), DonorRepresentativeName: "Donor representative",
						ActualItemCount: 2, VerificationHash: strings.Repeat("A", 64),
					}, metadata)
				}
				first, err = invoke()
				if err != nil {
					t.Fatal(err)
				}
				expectedOutbox := 2
				if failed {
					expectedOutbox = 3 // CollectionFailed followed by the replacement assignment.
				}
				if len(repo.outbox) != expectedOutbox || len(repo.handoffs) != 1 {
					t.Fatal("pickup must enqueue its event(s) and record one handoff")
				}
				eventType, topic := model.CollectionCompletedEventType, "batch.collection.completed"
				pickupAt := "2026-01-01T10:30:00.123456Z"
				if failed {
					eventType, topic = model.CollectionFailedEventType, "batch.collection.failed"
					pickupAt = "2026-01-01T11:00:00.123456Z"
				}
				data = validateC4ProducerEvent(t, repo.outbox[1], eventType, topic)
				if data["collector_scope_id"] != scopeID || data["pickup_occurred_at"] != pickupAt {
					t.Errorf("collection event lost historical scope or pickup time: %v", data)
				}
				if failed {
					replacement := repo.outbox[2]
					replacementData := validateC4ProducerEvent(t, replacement, model.CollectorAssignedEventType, "batch.collector.assigned")
					if replacement.SequenceInCommand != 2 || replacementData["previous_assignment_id"] != selection.Data.AssignmentID || replacementData["collector_user_id"] == "USR-001" {
						t.Fatalf("failed pickup did not move to the next collector: %+v", replacementData)
					}
					for _, field := range []string{"actual_item_count", "verification_hash"} {
						if _, present := data[field]; present {
							t.Errorf("failed pickup must not contain %s", field)
						}
					}
				}
			}
			eventCount, handoffCount := len(repo.outbox), len(repo.handoffs)
			replay, err := invoke()
			if err != nil || replay.EventID != first.EventID || len(repo.outbox) != eventCount || len(repo.handoffs) != handoffCount {
				t.Fatalf("command replay duplicated or changed its event: %v", err)
			}
		})
	}
}

func validateC4ProducerEvent(t *testing.T, event *model.EventOutbox, eventType, topic string) map[string]any {
	t.Helper()
	if event.EventType != eventType || event.Topic != topic || event.PartitionKey != event.BatchID {
		t.Fatalf("incorrect event routing: %+v", event)
	}
	if event.OccurredAt.Nanosecond()%1000 != 0 {
		t.Fatal("outbox timestamp must be microsecond-aligned before MySQL can round it")
	}
	value, err := matchingcontract.Decode(event.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	// Use the same approved-schema validator as eventbus.validateEvent.
	if err := matchingcontract.Validate(eventType, value); err != nil {
		t.Errorf("%s would be quarantined by the publisher: %v", eventType, err)
	}
	return value["data"].(map[string]any)
}
