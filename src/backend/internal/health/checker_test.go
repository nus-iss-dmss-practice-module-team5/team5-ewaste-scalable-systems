package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCheckerReturnsReadyWhenBothDependenciesRespond(t *testing.T) {
	checker := NewCheckerWithPingers(
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
		time.Second,
	)
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("expected ready dependencies: %v", err)
	}
}

func TestCheckerReturnsDependencyError(t *testing.T) {
	want := errors.New("mysql unavailable")
	checker := NewCheckerWithPingers(
		func(context.Context) error { return want },
		func(context.Context) error { t.Fatal("redis should not be checked after mysql failure"); return nil },
		time.Second,
	)
	if err := checker.Check(context.Background()); !errors.Is(err, want) {
		t.Fatalf("expected MySQL error, got %v", err)
	}
}

func TestCheckerHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checker := NewCheckerWithPingers(
		func(ctx context.Context) error { return ctx.Err() },
		func(context.Context) error { return nil },
		time.Second,
	)
	if err := checker.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled context, got %v", err)
	}
}

func TestCheckerDetailedReturnsDependencyError(t *testing.T) {
	checker := NewCheckerWithPingers(
		func(context.Context) error { return errors.New("mysql unavailable") },
		func(context.Context) error { return nil },
		time.Second,
	)

	_, err := checker.CheckDetailed(context.Background())
	if err == nil {
		t.Fatal("expected dependency error")
	}
}

func TestCheckerDetailedReportsEachDependency(t *testing.T) {
	checker := NewCheckerWithPingers(
		func(context.Context) error { return errors.New("mysql unavailable") },
		func(context.Context) error { return nil },
		time.Second,
	)

	report, _ := checker.CheckDetailed(context.Background())
	if report.Status != "not_ready" || report.MySQL != "unavailable" || report.Redis != "ok" {
		t.Fatalf("unexpected health report: %+v", report)
	}
}
