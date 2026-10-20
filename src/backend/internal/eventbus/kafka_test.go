package eventbus

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"

	"workflow-api/internal/config"
)

func TestNewKafkaPublisherConfiguresEventHubsTransport(t *testing.T) {
	publisher, err := NewKafkaPublisher(config.KafkaConfig{
		Brokers:        []string{"evh-ewaste-dev.servicebus.windows.net:9093"},
		TLSEnabled:     true,
		SASLMechanism:  "PLAIN",
		SASLUsername:   "$ConnectionString",
		SASLPassword:   "Endpoint=sb://evh-ewaste-dev.servicebus.windows.net/;SharedAccessKey=redacted",
		PublishTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create Event Hubs publisher: %v", err)
	}

	transport, ok := publisher.writer.Transport.(*kafka.Transport)
	if !ok {
		t.Fatalf("expected kafka.Transport, got %T", publisher.writer.Transport)
	}
	if transport == nil || transport.TLS == nil {
		t.Fatal("expected TLS transport for Event Hubs")
	}
	if transport.TLS.MinVersion != tls.VersionTLS12 || transport.TLS.ServerName != "evh-ewaste-dev.servicebus.windows.net" {
		t.Fatalf("unexpected TLS configuration: %+v", transport.TLS)
	}
	if transport.SASL == nil || transport.SASL.Name() != "PLAIN" {
		t.Fatal("expected SASL/PLAIN authentication for Event Hubs")
	}
}

func TestNewKafkaPublisherRejectsIncompleteSASLCredentials(t *testing.T) {
	_, err := NewKafkaPublisher(config.KafkaConfig{
		Brokers:      []string{"localhost:9092"},
		SASLUsername: "$ConnectionString",
	})
	if err == nil {
		t.Fatal("expected incomplete SASL credentials to be rejected")
	}
}

func TestKafkaServerNameSupportsHostAndHostPort(t *testing.T) {
	if got := kafkaServerName("broker.example.com:9093"); got != "broker.example.com" {
		t.Fatalf("expected host without port, got %q", got)
	}
	if got := kafkaServerName("broker.example.com"); got != "broker.example.com" {
		t.Fatalf("expected host to remain unchanged, got %q", got)
	}
}

// Preserve the matcher checks while keeping PR #41's transport tests above.
func TestMatcherEventHubsTransportSafeguards(t *testing.T) {
	cfg := config.KafkaConfig{Brokers: []string{"test.servicebus.windows.net:9093"}, TLSEnabled: true,
		SASLMechanism: "PLAIN", SASLUsername: "$ConnectionString",
		SASLPassword: "Endpoint=sb://test.servicebus.windows.net/;SharedAccessKeyName=test;SharedAccessKey=test"}
	p, err := NewKafkaPublisher(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if p.writer.RequiredAcks != kafka.RequireAll || p.writer.AllowAutoTopicCreation || p.writer.Async {
		t.Fatal("matcher requires synchronous acknowledged publication without topic creation")
	}
	for _, tc := range []struct {
		name  string
		alter func(*config.KafkaConfig)
	}{
		{"missing password", func(c *config.KafkaConfig) { c.SASLPassword = "" }},
		{"missing credentials", func(c *config.KafkaConfig) { c.SASLUsername = ""; c.SASLPassword = "" }},
		{"plaintext credentials", func(c *config.KafkaConfig) { c.TLSEnabled = false }},
		{"wrong Event Hubs username", func(c *config.KafkaConfig) { c.SASLUsername = "worker" }},
		{"unsupported mechanism", func(c *config.KafkaConfig) { c.SASLMechanism = "SCRAM-SHA-256" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := cfg
			tc.alter(&bad)
			if p, err := NewKafkaPublisher(bad); err == nil {
				if closeErr := p.Close(); closeErr != nil {
					t.Logf("close invalid publisher: %v", closeErr)
				}
				t.Fatal("invalid transport configuration accepted")
			}
		})
	}
}
