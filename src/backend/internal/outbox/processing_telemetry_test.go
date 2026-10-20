package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"workflow-api/internal/model"
)

type telemetryPermanentError struct{}

func (telemetryPermanentError) Error() string   { return "payload must never be logged" }
func (telemetryPermanentError) Permanent() bool { return true }

type telemetryRepo struct {
	fixtureRepository
	failAck bool
}

func (r *telemetryRepo) MarkPublished(ctx context.Context, id string, at time.Time) error {
	if r.failAck {
		return errors.New("private database detail")
	}
	return r.fixtureRepository.MarkPublished(ctx, id, at)
}
func TestDeliveryTelemetryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		failAck     bool
		observation string
	}{
		{"success", nil, false, "outbox_published"},
		{"retry", errors.New("private broker detail"), false, "outbox_retry"},
		{"quarantine", telemetryPermanentError{}, false, "outbox_quarantined"},
		{"persistence", nil, true, "outbox_persistence_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			repo := &telemetryRepo{fixtureRepository: fixtureRepository{event: model.EventOutbox{EventID: "event-1", BatchID: "batch-1", CommandID: "cmd-1", CorrelationID: "trace-1", EventType: "ReceiptVerified", AttemptCount: 2}}, failAck: tc.failAck}
			relay := NewRelay(repo, &fixturePublisher{err: tc.err}, nil, RelayConfig{}, zap.New(core))
			relay.processOnce(context.Background())
			entries := logs.FilterField(zap.String("observation", tc.observation)).All()
			if len(entries) != 1 {
				t.Fatal(logs.All())
			}
			f := entries[0].ContextMap()
			if f["batch_id"] != "batch-1" || f["correlation_id"] != "trace-1" || f["command_id"] != "cmd-1" || f["attempt"] != uint32(3) {
				t.Fatal(f)
			}
			if _, ok := f["error"]; ok {
				t.Fatal("unfiltered error logged")
			}
		})
	}
}
