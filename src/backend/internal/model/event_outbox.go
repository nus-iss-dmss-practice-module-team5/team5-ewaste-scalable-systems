package model

import "time"

const (
	RequestSubmittedEventType    = "RequestSubmitted"
	ClaimConfirmedEventType      = "ClaimConfirmed"
	CollectorAssignedEventType   = "CollectorAssigned"
	CollectionCompletedEventType = "CollectionCompleted"
	CollectionFailedEventType    = "CollectionFailed"
	ReceiptVerifiedEventType     = "ReceiptVerified"
	RecyclingCompletedEventType  = "RecyclingCompleted"
	RequestCompletedEventType    = "RequestCompleted"
)

type OutboxPublishState string

const (
	OutboxPublishStatePending     OutboxPublishState = "PENDING"
	OutboxPublishStatePublished   OutboxPublishState = "PUBLISHED"
	OutboxPublishStateQuarantined OutboxPublishState = "QUARANTINED"
)

type EventOutbox struct {
	EventID           string             `gorm:"column:event_id;primaryKey;size:36"`
	BatchID           string             `gorm:"column:batch_id;size:36;uniqueIndex:uq_outbox_command_event,priority:2;index:idx_outbox_batch,priority:1"`
	CommandID         string             `gorm:"column:command_id;size:36;uniqueIndex:uq_outbox_command_event,priority:1;uniqueIndex:uq_outbox_command_seq,priority:1"`
	EventType         string             `gorm:"column:event_type;size:48;uniqueIndex:uq_outbox_command_event,priority:3"`
	Topic             string             `gorm:"column:topic;size:128"`
	SchemaVersion     uint32             `gorm:"column:schema_version"`
	AggregateVersion  uint32             `gorm:"column:aggregate_version;index:idx_outbox_batch,priority:2"`
	SequenceInCommand uint32             `gorm:"column:sequence_in_command;uniqueIndex:uq_outbox_command_seq,priority:2;index:idx_outbox_batch,priority:3"`
	PartitionKey      string             `gorm:"column:partition_key;size:36"`
	PayloadJSON       []byte             `gorm:"column:payload_json;type:json"`
	CorrelationID     string             `gorm:"column:correlation_id;size:128"`
	OccurredAt        time.Time          `gorm:"column:occurred_at"`
	CreatedAt         time.Time          `gorm:"column:created_at"`
	PublishState      OutboxPublishState `gorm:"column:publish_state;size:16"`
	AttemptCount      uint32             `gorm:"column:attempt_count"`
	NextAttemptAt     *time.Time         `gorm:"column:next_attempt_at"`
	PublishedAt       *time.Time         `gorm:"column:published_at"`
	LastErrorCode     *string            `gorm:"column:last_error_code;size:64"`
}

func (EventOutbox) TableName() string { return "event_outbox" }
