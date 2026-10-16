package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCollectorRotationPreservesCursorAndWrapOrder(t *testing.T) {
	for _, user := range []string{"USR-001", "USR-002", "USR-003"} {
		t.Run(user, func(t *testing.T) {
			db, mock := newBatchRepositoryTestDB(t)
			now := time.Now().UTC()
			order := "ORDER BY CASE WHEN scope.collector_org_id > ? OR (scope.collector_org_id = ? AND collector.user_id > ?) THEN 0 ELSE 1 END, scope.collector_org_id, collector.user_id, scope.id LIMIT ? FOR UPDATE"
			mock.ExpectQuery(regexp.QuoteMeta(order)).
				WithArgs("REC-001", "NORTH", now, now, "COL-001", "COL-001", user, 1).
				WillReturnRows(sqlmock.NewRows([]string{"user_id", "organisation_id", "scope_id", "scope_version"}))
			_, err := findNextActiveCollector(context.Background(), db, "REC-001", "NORTH", &CollectorCandidate{UserID: user, OrganisationID: "COL-001"}, "", now)
			if err != ErrAssignmentNoActiveCollector {
				t.Fatalf("query failed or empty result was accepted: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCollectorRotationExcludesPreviousCollector(t *testing.T) {
	db, mock := newBatchRepositoryTestDB(t)
	now := time.Now().UTC()
	mock.ExpectQuery("collector.user_id <> \\?.*ORDER BY CASE WHEN.*END, scope.collector_org_id, collector.user_id, scope.id LIMIT \\? FOR UPDATE").
		WithArgs("REC-001", "NORTH", now, now, "USR-002", "COL-001", "COL-001", "USR-002", 1).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "organisation_id", "scope_id", "scope_version"}).AddRow("USR-003", "COL-001", "scope-1", 2))
	candidate, err := findNextActiveCollector(context.Background(), db, "REC-001", "NORTH", &CollectorCandidate{UserID: "USR-002", OrganisationID: "COL-001"}, "USR-002", now)
	if err != nil || candidate.UserID != "USR-003" || candidate.ScopeVersion != 2 {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
