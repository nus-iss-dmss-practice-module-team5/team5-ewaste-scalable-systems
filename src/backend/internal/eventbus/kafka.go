package eventbus

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/plain"

	"workflow-api/internal/config"
	"workflow-api/internal/matchingcontract"
	"workflow-api/internal/model"
)

var (
	ErrKafkaPublisherUnavailable = errors.New("kafka publisher is unavailable")
	ErrKafkaTopicMissing         = errors.New("kafka topic is missing")
	ErrKafkaPayloadMissing       = errors.New("kafka payload is missing")
)

// InvalidEventError identifies an immutable outbox record that cannot be
// safely published. These records are quarantined instead of retried.
type InvalidEventError struct {
	cause error
}

func (e *InvalidEventError) Error() string {
	return e.cause.Error()
}

func (e *InvalidEventError) Unwrap() error {
	return e.cause
}

func (e *InvalidEventError) Permanent() bool {
	return true
}

type KafkaPublisher struct {
	writer *kafka.Writer
}

var canonicalTopicByEventType = map[string]string{
	"RequestSubmitted":    "ewaste.batch.events",
	"MatchingCompleted":   "ewaste.batch.events",
	"ClaimConfirmed":      "ewaste.claim.events",
	"CollectorAssigned":   "batch.collector.assigned",
	"CollectionCompleted": "batch.collection.completed",
	"CollectionFailed":    "batch.collection.failed",
	"ReceiptVerified":     "ewaste.batch.events",
	"RecyclingCompleted":  "ewaste.batch.events",
	"RequestCompleted":    "ewaste.batch.events",
}

func NewKafkaPublisher(cfg config.KafkaConfig) (*KafkaPublisher, error) {
	brokers := make([]string, 0, len(cfg.Brokers))

	for _, broker := range cfg.Brokers {
		broker = strings.TrimSpace(broker)
		if broker != "" {
			brokers = append(brokers, broker)
		}
	}

	if len(brokers) == 0 {
		return nil, errors.New("kafka brokers are not configured")
	}

	publishTimeout := cfg.PublishTimeout
	if publishTimeout <= 0 {
		publishTimeout = 10 * time.Second
	}

	clientID := strings.TrimSpace(cfg.ClientID)
	if clientID == "" {
		clientID = "workflow-api"
	}

	transport := &kafka.Transport{
		DialTimeout: publishTimeout,
		ClientID:    clientID,
	}

	if cfg.TLSEnabled {
		// Event Hubs requires TLS 1.2 or newer for its Kafka endpoint.
		// ServerName is set explicitly so certificate verification remains
		// enabled when the broker address is supplied as host:port.
		transport.TLS = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: kafkaServerName(brokers[0]),
		}
	}

	saslUsername := strings.TrimSpace(cfg.SASLUsername)
	saslPassword := cfg.SASLPassword
	// Keep the matcher's fail-closed transport rules on PR #41's native fields.
	if (saslUsername != "" || saslPassword != "") && !cfg.TLSEnabled {
		return nil, errors.New("kafka SASL credentials require TLS")
	}
	for _, broker := range brokers {
		host := strings.TrimSuffix(strings.ToLower(kafkaServerName(broker)), ".")
		if strings.HasSuffix(host, ".servicebus.windows.net") &&
			(!cfg.TLSEnabled || saslUsername != "$ConnectionString" || saslPassword == "") {
			return nil, errors.New("event hubs requires TLS and connection-string SASL credentials")
		}
	}
	if saslUsername != "" || saslPassword != "" {
		if saslUsername == "" || saslPassword == "" {
			return nil, errors.New("kafka sasl username and password must be configured together")
		}

		mechanism := strings.ToUpper(strings.TrimSpace(cfg.SASLMechanism))
		if mechanism == "" {
			mechanism = "PLAIN"
		}
		if mechanism != "PLAIN" {
			return nil, fmt.Errorf("unsupported kafka sasl mechanism %q", mechanism)
		}
		transport.SASL = plain.Mechanism{
			Username: saslUsername,
			Password: saslPassword,
		}
	}

	return &KafkaPublisher{
		// WriterConfig/NewWriter are deprecated in kafka-go v0.4.51.
		// Configure Writer directly so broker acknowledgements remain explicit.
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(brokers...),
			Balancer:               &kafka.Hash{},
			RequiredAcks:           kafka.RequireAll,
			BatchSize:              1,
			MaxAttempts:            cfg.MaxAttempts,
			ReadTimeout:            publishTimeout,
			WriteTimeout:           publishTimeout,
			Async:                  false,
			Transport:              transport,
			AllowAutoTopicCreation: false,
		},
	}, nil
}

func kafkaServerName(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	return strings.Trim(address, "[]")
}

// ValidateEvent checks the schema and routing identity before publication.
// The payload is sent as persisted, never reconstructed from live tables.
func ValidateEvent(event model.EventOutbox) error {
	body, err := matchingcontract.Decode(event.PayloadJSON)
	if err != nil || matchingcontract.Validate(event.EventType, body) != nil {
		return &InvalidEventError{cause: errors.New("persisted event violates approved schema")}
	}
	if strings.TrimSpace(event.Topic) == "" {
		return &InvalidEventError{cause: ErrKafkaTopicMissing}
	}
	if len(event.PayloadJSON) == 0 || !json.Valid(event.PayloadJSON) {
		return &InvalidEventError{cause: ErrKafkaPayloadMissing}
	}
	if event.EventID == "" || event.CommandID == "" || event.BatchID == "" {
		return &InvalidEventError{
			cause: errors.New("kafka event identifiers are incomplete"),
		}
	}
	if event.PartitionKey != event.BatchID {
		return &InvalidEventError{
			cause: errors.New("kafka partition key must equal batch id"),
		}
	}
	expectedTopic, knownEventType := canonicalTopicByEventType[event.EventType]
	if !knownEventType || event.Topic != expectedTopic {
		return &InvalidEventError{
			cause: errors.New("kafka event type and topic do not agree"),
		}
	}
	if event.SchemaVersion != 1 {
		return &InvalidEventError{
			cause: errors.New("unsupported kafka schema version"),
		}
	}
	if event.SequenceInCommand == 0 {
		return &InvalidEventError{
			cause: errors.New("kafka event sequence must be positive"),
		}
	}
	if len(event.CorrelationID) == 0 || utf8.RuneCountInString(event.CorrelationID) > 128 {
		return &InvalidEventError{
			cause: errors.New("kafka correlation id is invalid"),
		}
	}

	for _, value := range []string{
		event.EventID,
		event.CommandID,
		event.BatchID,
		event.PartitionKey,
	} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != value {
			return &InvalidEventError{
				cause: errors.New("kafka identifier must be a lowercase uuid"),
			}
		}
	}

	if isProcessingEvent(event.EventType) {
		return validateProcessingEnvelope(event)
	}

	var envelope struct {
		EventID           string          `json:"event_id"`
		EventType         string          `json:"event_type"`
		SchemaVersion     uint32          `json:"schema_version"`
		CommandID         string          `json:"command_id"`
		BatchID           string          `json:"batch_id"`
		BatchVersion      uint32          `json:"batch_version"`
		ClaimEpoch        string          `json:"claim_epoch"`
		SequenceInCommand uint32          `json:"sequence_in_command"`
		OccurredAt        time.Time       `json:"occurred_at"`
		CorrelationID     string          `json:"correlation_id"`
		Data              json.RawMessage `json:"data"`
	}

	if err := json.Unmarshal(event.PayloadJSON, &envelope); err != nil {
		return &InvalidEventError{cause: ErrKafkaPayloadMissing}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return &InvalidEventError{cause: errors.New("kafka event data is missing")}
	}
	if envelope.EventID != event.EventID ||
		envelope.EventType != event.EventType ||
		envelope.SchemaVersion != event.SchemaVersion ||
		envelope.CommandID != event.CommandID ||
		envelope.BatchID != event.BatchID ||
		envelope.BatchVersion != event.AggregateVersion ||
		envelope.SequenceInCommand != event.SequenceInCommand ||
		envelope.CorrelationID != event.CorrelationID ||
		envelope.OccurredAt.UTC().UnixMicro() != event.OccurredAt.UTC().UnixMicro() ||
		envelope.ClaimEpoch == "" {
		return &InvalidEventError{
			cause: errors.New("kafka payload metadata does not match outbox metadata"),
		}
	}

	return nil
}

func isProcessingEvent(eventType string) bool {
	switch eventType {
	case model.ReceiptVerifiedEventType, model.RecyclingCompletedEventType, model.RequestCompletedEventType:
		return true
	default:
		return false
	}
}

func validateProcessingEnvelope(event model.EventOutbox) error {
	var envelope struct {
		EventID           string          `json:"event_id"`
		EventType         string          `json:"event_type"`
		SchemaVersion     uint32          `json:"schema_version"`
		Producer          string          `json:"producer"`
		AggregateType     string          `json:"aggregate_type"`
		AggregateID       string          `json:"aggregate_id"`
		AggregateVersion  uint32          `json:"aggregate_version"`
		CommandID         string          `json:"command_id"`
		BatchID           string          `json:"batch_id"`
		ClaimEpoch        string          `json:"claim_epoch"`
		SequenceInCommand uint32          `json:"sequence_in_command"`
		OccurredAt        time.Time       `json:"occurred_at"`
		CorrelationID     string          `json:"correlation_id"`
		Data              json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(event.PayloadJSON, &envelope); err != nil {
		return &InvalidEventError{cause: ErrKafkaPayloadMissing}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return &InvalidEventError{cause: errors.New("kafka event data is missing")}
	}
	if envelope.EventID != event.EventID ||
		envelope.EventType != event.EventType ||
		envelope.SchemaVersion != event.SchemaVersion ||
		envelope.Producer == "" ||
		envelope.AggregateType != "EWasteBatch" ||
		envelope.AggregateID != event.BatchID ||
		envelope.AggregateVersion != event.AggregateVersion ||
		envelope.CommandID != event.CommandID ||
		envelope.BatchID != event.BatchID ||
		envelope.SequenceInCommand != event.SequenceInCommand ||
		envelope.CorrelationID != event.CorrelationID ||
		envelope.OccurredAt.UTC().UnixMicro() != event.OccurredAt.UTC().UnixMicro() ||
		envelope.ClaimEpoch == "" {
		return &InvalidEventError{cause: errors.New("kafka processing payload metadata does not match outbox metadata")}
	}
	return nil
}

func (p *KafkaPublisher) Publish(
	ctx context.Context,
	event model.EventOutbox,
) error {
	if p == nil || p.writer == nil {
		return ErrKafkaPublisherUnavailable
	}
	if err := ValidateEvent(event); err != nil {
		return err
	}

	return p.writer.WriteMessages(ctx, kafka.Message{
		Topic: event.Topic,
		Key:   []byte(event.PartitionKey),
		Value: event.PayloadJSON,
		Headers: []kafka.Header{
			{
				Key:   "event_id",
				Value: []byte(event.EventID),
			},
			{
				Key:   "event_type",
				Value: []byte(event.EventType),
			},
			{
				Key:   "schema_version",
				Value: []byte(strconv.FormatUint(uint64(event.SchemaVersion), 10)),
			},
			{
				Key:   "correlation_id",
				Value: []byte(event.CorrelationID),
			},
		},
	})
}

func (p *KafkaPublisher) Close() error {
	if p == nil || p.writer == nil {
		return nil
	}

	return p.writer.Close()
}
