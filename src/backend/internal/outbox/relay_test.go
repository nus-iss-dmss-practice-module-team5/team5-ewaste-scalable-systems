package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"workflow-api/internal/model"
)

type fixtureRepository struct {
	event                           model.EventOutbox
	retried, quarantined, published int
}

func (f *fixtureRepository) ClaimPending(context.Context, time.Time, int, time.Duration) ([]model.EventOutbox, error) {
	return []model.EventOutbox{f.event}, nil
}
func (f *fixtureRepository) MarkPublished(context.Context, string, time.Time) error {
	f.published++
	return nil
}
func (f *fixtureRepository) MarkRetry(context.Context, string, time.Time, string) error {
	f.retried++
	return nil
}
func (f *fixtureRepository) MarkQuarantined(context.Context, string, string) error {
	f.quarantined++
	return nil
}

type fixturePublisher struct {
	block bool
	err   error
}

func (f *fixturePublisher) Publish(ctx context.Context, _ model.EventOutbox) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}
func (*fixturePublisher) Close() error { return nil }

type lostLease struct{}

func (lostLease) Renew(context.Context, time.Duration) error { return errors.New("lost") }
func (lostLease) Release(context.Context) error              { return nil }
func TestTransientOutageRetainsRecoverableIntent(t *testing.T) {
	repo := &fixtureRepository{event: model.EventOutbox{AttemptCount: 100}}
	publisher := &fixturePublisher{err: errors.New("broker unavailable")}
	relay := NewRelay(repo, publisher, nil, RelayConfig{MaxAttempts: 3}, nil)
	relay.processOnce(context.Background())
	if repo.retried != 1 || repo.quarantined != 0 {
		t.Fatal("temporary outage quarantined durable intent")
	}
	publisher.err = nil
	relay.processOnce(context.Background())
	if repo.published != 1 {
		t.Fatal("recovery did not publish")
	}
}
func TestLeaseLossCancelsBlockedPublication(t *testing.T) {
	repo := &fixtureRepository{}
	relay := NewRelay(repo, &fixturePublisher{block: true}, nil, RelayConfig{LeaderLeaseDuration: 30 * time.Millisecond, PublishTimeout: time.Minute}, nil)
	done := make(chan struct{})
	go func() { relay.runAsLeader(context.Background(), lostLease{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel broker call")
	}
	if repo.published != 0 || repo.retried != 0 || repo.quarantined != 0 {
		t.Fatal("lost leader changed outbox state")
	}
}
