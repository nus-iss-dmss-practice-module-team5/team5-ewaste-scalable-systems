package model

import "time"

type CommandState string

const (
	CommandStateInProgress CommandState = "IN_PROGRESS"
	CommandStateCompleted  CommandState = "COMPLETED"
)

// CommandIdempotency is the durable replay authority for workflow commands.
// Replay identity is actor scope + command name + idempotency key.
type CommandIdempotency struct {
	ID               string       `gorm:"column:id;primaryKey;size:36"`
	ActorUserID      *string      `gorm:"column:actor_user_id;size:32"`
	ServicePrincipal *string      `gorm:"column:service_principal;size:128"`
	ActorScope       string       `gorm:"column:actor_scope;size:160;uniqueIndex:uq_command_replay,priority:1"`
	CommandName      string       `gorm:"column:command_name;size:64;uniqueIndex:uq_command_replay,priority:2"`
	IdempotencyKey   string       `gorm:"column:idempotency_key;size:64;uniqueIndex:uq_command_replay,priority:3"`
	RequestHash      string       `gorm:"column:request_hash;size:64"`
	BatchID          *string      `gorm:"column:batch_id;size:36;index"`
	AssignmentID     *string      `gorm:"column:assignment_id;size:36"`
	State            CommandState `gorm:"column:state;size:16"`
	ResponseStatus   *int         `gorm:"column:response_status"`
	ResponseJSON     []byte       `gorm:"column:response_json;type:json"`
	CreatedAt        time.Time    `gorm:"column:created_at"`
	CompletedAt      *time.Time   `gorm:"column:completed_at"`
	RetainUntil      time.Time    `gorm:"column:retain_until"`
}

func (CommandIdempotency) TableName() string { return "command_idempotency" }
