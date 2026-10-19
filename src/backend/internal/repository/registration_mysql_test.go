package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"workflow-api/internal/model"
)

// Real MySQL check of the registration transaction, constraints and
// duplicate-key mapping. CI's matcher job runs it against its migrated, seeded
// matcher_test database; the case runs inside an outer rollback transaction, so
// the database is left unchanged.
func registrationMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("REGISTRATION_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("MATCHER_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("set REGISTRATION_TEST_DSN or MATCHER_TEST_DSN to a migrated, seeded disposable *_test database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var name string
	if err := db.Raw("SELECT DATABASE()").Scan(&name).Error; err != nil || !strings.HasSuffix(name, "_test") {
		t.Fatalf("requires a disposable *_test database: %q %v", name, err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func TestRegistrationRepositoryMySQLLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := NewGormRegistrationRepository(registrationMySQL(t))

	org := &model.Organisation{OrganisationID: "COL-EWSS28TEST0001", OrganisationName: "EWSS-28 Test Collect", OrganisationType: "COLLECTION_OPERATOR", Status: "PENDING"}
	user := &model.User{UserID: "USR-EWSS28TEST0001", Email: "ewss28-test@example.com", DisplayName: "EWSS-28", PasswordHash: "$2a$04$placeholderplaceholderplaceholderplaceholderplace", RoleCode: "COLLECTOR", OrganisationID: org.OrganisationID, Status: "PENDING"}
	if err := repo.CreateRegistration(ctx, org, user); err != nil {
		t.Fatalf("create registration: %v", err)
	}

	duplicateOrg := &model.Organisation{OrganisationID: "COL-EWSS28TEST0002", OrganisationName: "EWSS-28 Second Org", OrganisationType: "COLLECTION_OPERATOR", Status: "PENDING"}
	duplicateUser := *user
	duplicateUser.UserID, duplicateUser.OrganisationID = "USR-EWSS28TEST0002", duplicateOrg.OrganisationID
	if err := repo.CreateRegistration(ctx, duplicateOrg, &duplicateUser); !errors.Is(err, ErrDuplicateRegistration) {
		t.Fatalf("duplicate email: expected ErrDuplicateRegistration, got %v", err)
	}

	pending, err := repo.ListPendingRegistrations(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	found := false
	for _, row := range pending {
		if row.UserID == user.UserID {
			found = row.OrganisationName == org.OrganisationName && row.OrganisationType == "COLLECTION_OPERATOR"
		}
		if row.UserID == duplicateUser.UserID {
			t.Fatal("the rolled-back duplicate registration is listed")
		}
	}
	if !found {
		t.Fatalf("pending registration not listed with its organisation: %+v", pending)
	}

	review := &model.RegistrationReview{ReviewID: "00000000-0000-4000-8000-0000000ee028", UserID: user.UserID, Decision: "APPROVED", ReviewedBy: "USR-001", CorrelationID: "ewss28-mysql", ReviewedAt: time.Now().UTC()}
	if err := repo.DecideRegistration(ctx, review, "ACTIVE", "ACTIVE"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if review.OrganisationID != org.OrganisationID {
		t.Fatalf("review not bound to the organisation: %+v", review)
	}
	var statuses struct{ UserStatus, OrgStatus string }
	if err := repo.db.Raw("SELECT u.status AS user_status, o.status AS org_status FROM users u JOIN organisations o ON o.organisation_id = u.organisation_id WHERE u.user_id = ?", user.UserID).Scan(&statuses).Error; err != nil {
		t.Fatal(err)
	}
	if statuses.UserStatus != "ACTIVE" || statuses.OrgStatus != "ACTIVE" {
		t.Fatalf("approval not persisted: %+v", statuses)
	}

	second := &model.RegistrationReview{ReviewID: "00000000-0000-4000-8000-0000000ee029", UserID: user.UserID, Decision: "REJECTED", ReviewedBy: "USR-001", CorrelationID: "ewss28-mysql-2", ReviewedAt: time.Now().UTC()}
	if err := repo.DecideRegistration(ctx, second, "DISABLED", "REJECTED"); !errors.Is(err, ErrRegistrationNotPending) {
		t.Fatalf("second decision: expected ErrRegistrationNotPending, got %v", err)
	}
	unknown := &model.RegistrationReview{ReviewID: "00000000-0000-4000-8000-0000000ee030", UserID: "USR-DOES-NOT-EXIST", Decision: "APPROVED", ReviewedBy: "USR-001", CorrelationID: "ewss28-mysql-3", ReviewedAt: time.Now().UTC()}
	if err := repo.DecideRegistration(ctx, unknown, "ACTIVE", "ACTIVE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: expected ErrNotFound, got %v", err)
	}
}
