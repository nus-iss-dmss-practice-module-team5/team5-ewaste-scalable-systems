// Package app owns process composition for each independently deployable Go service.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"workflow-api/internal/config"
	"workflow-api/internal/controller"
	"workflow-api/internal/eventbus"
	"workflow-api/internal/health"
	"workflow-api/internal/lease"
	"workflow-api/internal/logger"
	"workflow-api/internal/matching"
	"workflow-api/internal/outbox"
	"workflow-api/internal/ratelimit"
	"workflow-api/internal/repository"
	"workflow-api/internal/router"
	"workflow-api/internal/service"
	"workflow-api/internal/storage"
	"workflow-api/internal/token"
)

// Run starts a bounded-context HTTP service. It intentionally shares platform
// setup and the existing MySQL transaction implementation; the router is the
// process boundary in this first extraction step.
func Run(serviceName router.Service) error {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load .env: %w", err)
	}

	modeFlag := flag.String("mode", "", "application mode: development, test, or production")
	checkDependencies := flag.Bool("check-dependencies", false, "connect to MySQL and Redis, then exit")
	flag.Parse()

	cfg, err := config.Load(os.Getenv("EWASTE_CONFIG_FILE"))
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.ApplyMode(*modeFlag); err != nil {
		return fmt.Errorf("apply mode: %w", err)
	}
	if err := cfg.ApplyMode(cfg.Mode); err != nil {
		return fmt.Errorf("apply configured mode: %w", err)
	}

	appLogger, err := logger.New(cfg.Logging)
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer func() {
		if closeErr := appLogger.Close(); closeErr != nil {
			log.Printf("close logger: %v", closeErr)
		}
	}()

	if cfg.Database.Host == "" || cfg.Database.Port == 0 || cfg.Database.Name == "" || cfg.Database.User == "" || cfg.Database.Password == "" ||
		(!*checkDependencies && (cfg.Auth.AccessSecret == "" || cfg.Auth.RefreshSecret == "" || cfg.Auth.RefreshHashSecret == "")) {
		return errors.New("database DSN and all auth secrets must be configured")
	}

	db, err := storage.OpenMySQL(cfg.Database)
	if err != nil {
		appLogger.Error("mysql connection failed", zap.Error(err))
		return errors.New("connect to mysql")
	}
	if err := storage.PingMySQL(db, 5*time.Second); err != nil {
		appLogger.Error("mysql ping failed", zap.Error(err))
		return errors.New("ping mysql")
	}

	redisOptions := &redis.Options{Addr: cfg.Redis.Address, Password: cfg.Redis.Password, DB: cfg.Redis.DB}
	if cfg.Redis.TLSEnabled {
		redisOptions.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	redisClient := redis.NewClient(redisOptions)
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			appLogger.Warn("close redis client", zap.Error(closeErr))
		}
	}()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = redisClient.Ping(pingCtx).Err()
	cancel()
	if err != nil {
		appLogger.Error("redis ping failed", zap.Error(err))
		return errors.New("connect to redis")
	}
	if *checkDependencies {
		appLogger.Info("local dependencies are available", zap.String("service", string(serviceName)), zap.String("mode", cfg.Mode))
		return nil
	}

	tokens := token.NewService(cfg.Auth.Issuer, cfg.Auth.AccessSecret, cfg.Auth.RefreshSecret, cfg.Auth.RefreshHashSecret, cfg.Auth.AccessTTL, cfg.Auth.RefreshTTL)
	authRepo := repository.NewGormAuthRepository(db)
	deps := router.ServiceDependencies{
		Tokens: tokens, AuthRepository: authRepo, Limiter: ratelimit.NewRedisLimiter(redisClient, cfg.RateLimit.Requests, cfg.RateLimit.Window),
		Checker: health.NewChecker(db, redisClient, 3*time.Second), AllowedOrigins: cfg.Server.AllowedOrigins, Logger: appLogger.Logger,
		AnalyticsServiceToken: cfg.Auth.AnalyticsServiceToken,
	}

	switch serviceName {
	case router.IdentityAccessService:
		deps.AuthController = controller.NewAuthController(service.NewAuthService(authRepo, tokens, appLogger.Logger), appLogger.Logger)
	case router.BatchWorkflowService:
		batchRepo := repository.NewGormBatchRepository(db)
		batchService := service.NewBatchServiceWithAnalyticsRuleVersion(batchRepo, cfg.Analytics.ApprovedRuleVersion)
		deps.BatchController = controller.NewBatchController(batchService, appLogger.Logger)
		deps.AnalyticsController = controller.NewAnalyticsController(batchService, appLogger.Logger)
		deps.WorkflowReadController = newWorkflowReadController(db, appLogger.Logger)
		if err := registerBatchRelay(&cfg, db, redisClient, appLogger.Logger); err != nil {
			return err
		}
	case router.ClaimLogisticsService:
		claimRepo := repository.NewGormClaimRepository(db)
		claimLease := lease.NewRedisBatchLease(redisClient, 3*time.Second, 200*time.Millisecond)
		deps.ClaimController = controller.NewClaimController(service.NewClaimWorkflowService(claimRepo, claimLease), appLogger.Logger)
		deps.AssignmentController = controller.NewAssignmentController(service.NewAssignmentWorkflowService(repository.NewGormAssignmentRepository(db)), appLogger.Logger)
		deps.WorkflowReadController = newWorkflowReadController(db, appLogger.Logger)
	case router.CustodyVerificationService:
		batchRepo := repository.NewGormBatchRepository(db)
		deps.BatchController = controller.NewBatchController(service.NewBatchServiceWithAnalyticsRuleVersion(batchRepo, cfg.Analytics.ApprovedRuleVersion), appLogger.Logger)
		deps.AssignmentController = controller.NewAssignmentController(service.NewAssignmentWorkflowService(repository.NewGormAssignmentRepository(db)), appLogger.Logger)
		evidenceStorage, storageErr := storage.NewEvidenceStorage(cfg.Storage)
		if storageErr != nil {
			return fmt.Errorf("create evidence storage: %w", storageErr)
		}
		evidenceService := service.NewEvidenceService(batchRepo, evidenceStorage)
		evidenceService.SetMaxUploadSizeBytes(cfg.Storage.MaxUploadSizeBytes)
		deps.EvidenceController = controller.NewEvidenceController(evidenceService, appLogger.Logger)
		deps.WorkflowReadController = newWorkflowReadController(db, appLogger.Logger)
	default:
		return fmt.Errorf("unsupported workflow service %q", serviceName)
	}

	appRouter, err := router.NewServiceRouter(serviceName, deps)
	if err != nil {
		return err
	}
	if serviceName == router.BatchWorkflowService {
		if err := matching.RegisterFromEnv(appRouter, db); err != nil {
			return fmt.Errorf("configure matching facade: %w", err)
		}
	}

	appLogger.Info("workflow service listening", zap.String("service", string(serviceName)), zap.String("address", cfg.Server.Port))
	if err := appRouter.Run(cfg.Server.Port); err != nil {
		return fmt.Errorf("server stopped: %w", err)
	}
	return nil
}

func newWorkflowReadController(db *gorm.DB, appLogger *zap.Logger) *controller.WorkflowReadController {
	workflowRead := service.NewWorkflowReadService(repository.NewGormWorkflowReadRepository(db))
	auditorRead := service.NewAuditorReadService(repository.NewGormAuditorReadRepository(db))
	return controller.NewWorkflowReadController(workflowRead, appLogger, auditorRead)
}

// registerBatchRelay deliberately belongs to Batch Workflow only. Other
// binaries may write outbox rows through the shared transaction port, but they
// must never start another relay leader loop.
func registerBatchRelay(cfg *config.Config, db *gorm.DB, redisClient *redis.Client, appLogger *zap.Logger) error {
	if !cfg.Kafka.Enabled {
		return nil
	}
	publisher, err := eventbus.NewKafkaPublisher(cfg.Kafka)
	if err != nil {
		return fmt.Errorf("create kafka publisher: %w", err)
	}
	relay := outbox.NewRelay(
		repository.NewGormOutboxRepository(db),
		publisher,
		outbox.NewRedisLeaderLeaseFactory(redisClient, "ewaste:workflow-api:event-outbox-relay"),
		outbox.RelayConfig{
			PollInterval: cfg.Kafka.PublishInterval, BatchSize: cfg.Kafka.BatchSize, MaxAttempts: cfg.Kafka.MaxAttempts,
			LeaseDuration: cfg.Kafka.LeaseDuration, LeaderLeaseDuration: cfg.Kafka.LeaderLeaseDuration,
			RetryBackoff: cfg.Kafka.RetryBackoff, PublishTimeout: cfg.Kafka.PublishTimeout,
		},
		appLogger,
	)
	go relay.Run(context.Background())
	appLogger.Info("kafka outbox relay started", zap.Strings("brokers", cfg.Kafka.Brokers))
	return nil
}
