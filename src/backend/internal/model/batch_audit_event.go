package model

import "time"

const (
	BatchAuditEventDraftSaved                 = "DraftSaved"
	BatchAuditEventRequestSubmitted           = "RequestSubmitted"
	BatchAuditEventClaimConfirmed             = "ClaimConfirmed"
	BatchAuditEventCollectorAssigned          = "CollectorAssigned"
	BatchAuditEventAssignmentAccepted         = "AssignmentAccepted"
	BatchAuditEventAssignmentRejected         = "AssignmentRejected"
	BatchAuditEventCollectionCompleted        = "CollectionCompleted"
	BatchAuditEventCollectionFailed           = "CollectionFailed"
	BatchAuditEventCollectionRecoveryApproved = "CollectionRecoveryApproved"
	BatchAuditEventTreatmentRecorded          = "TreatmentRecorded"
	BatchAuditEventEvidenceUploaded           = "EvidenceUploaded"
	BatchAuditEventEvidenceDownloaded         = "EvidenceDownloaded"
	BatchAuditEventAnalyticsCompleted         = "AnalyticsCompleted"
)

type BatchAuditEvent struct {
	ID                  string      `gorm:"column:id;primaryKey;size:36"`
	BatchID             string      `gorm:"column:batch_id;size:36;index"`
	CommandID           string      `gorm:"column:command_id;size:36;uniqueIndex:uq_batch_audit_command_seq,priority:1"`
	AssignmentID        *string     `gorm:"column:assignment_id;size:36"`
	ClaimID             *string     `gorm:"column:claim_id;size:36"`
	ActorUserID         *string     `gorm:"column:actor_user_id;size:32;index"`
	ActorOrganizationID *string     `gorm:"column:actor_org_id;size:32;index"`
	ServicePrincipal    *string     `gorm:"column:service_principal;size:128"`
	EventType           string      `gorm:"column:event_type;size:48"`
	FromStatus          BatchStatus `gorm:"column:from_status;size:32"`
	ToStatus            BatchStatus `gorm:"column:to_status;size:32"`
	BatchVersion        uint32      `gorm:"column:batch_version"`
	SequenceInCommand   uint32      `gorm:"column:sequence_in_command;uniqueIndex:uq_batch_audit_command_seq,priority:2"`
	OccurredAt          time.Time   `gorm:"column:occurred_at"`
	CorrelationID       string      `gorm:"column:correlation_id;size:128"`
	DetailsJSON         []byte      `gorm:"column:details_json;type:json"`
}

func (BatchAuditEvent) TableName() string { return "batch_audit_events" }
