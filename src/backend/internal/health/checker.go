package health

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Checker struct {
	mysqlPing func(context.Context) error
	redisPing func(context.Context) error
	timeout   time.Duration
}

type Report struct {
	Status string `json:"status"`
	MySQL  string `json:"mysql"`
	Redis  string `json:"redis"`
}

func NewChecker(db *gorm.DB, redisClient *redis.Client, timeout time.Duration) *Checker {
	return NewCheckerWithPingers(
		func(ctx context.Context) error {
			sqlDB, err := db.DB()
			if err != nil {
				return err
			}
			return sqlDB.PingContext(ctx)
		},
		func(ctx context.Context) error { return redisClient.Ping(ctx).Err() },
		timeout,
	)
}

func NewCheckerWithPingers(mysqlPing, redisPing func(context.Context) error, timeout time.Duration) *Checker {
	return &Checker{mysqlPing: mysqlPing, redisPing: redisPing, timeout: timeout}
}

func (c *Checker) Check(ctx context.Context) error {
	if c == nil || c.mysqlPing == nil || c.redisPing == nil {
		return errors.New("health checker is not configured")
	}
	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.mysqlPing(checkCtx); err != nil {
		return err
	}
	return c.redisPing(checkCtx)
}

// CheckDetailed checks both dependencies and returns a safe public health report.
// It intentionally does not include dependency error details.
func (c *Checker) CheckDetailed(ctx context.Context) (Report, error) {
	report := Report{Status: "ready", MySQL: "unavailable", Redis: "unavailable"}
	if c == nil || c.mysqlPing == nil || c.redisPing == nil {
		report.Status = "not_ready"
		return report, errors.New("health checker is not configured")
	}

	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var firstErr error
	if err := c.mysqlPing(checkCtx); err != nil {
		firstErr = err
	} else {
		report.MySQL = "ok"
	}
	if err := c.redisPing(checkCtx); err != nil {
		if firstErr == nil {
			firstErr = err
		}
	} else {
		report.Redis = "ok"
	}
	if firstErr != nil {
		report.Status = "not_ready"
	}
	return report, firstErr
}
