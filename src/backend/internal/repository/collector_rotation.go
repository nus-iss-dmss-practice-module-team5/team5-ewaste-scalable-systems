package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CollectorCandidate is one active collector that can serve a recycler in a
// zone. The deterministic ordering is the round-robin order.
type CollectorCandidate struct {
	UserID         string
	OrganisationID string
	ScopeID        string
	ScopeVersion   uint64
}

func findMostRecentCollector(ctx context.Context, db *gorm.DB, recyclerOrgID, zone string) (*CollectorCandidate, error) {
	var candidate CollectorCandidate
	err := db.WithContext(ctx).
		Table("batch_assignments AS assignment").
		Select("assignment.collector_user_id AS user_id, assignment.collector_org_id AS organisation_id, assignment.collector_scope_id AS scope_id").
		Joins("INNER JOIN ewaste_batches AS batch ON batch.id = assignment.batch_id").
		Where("assignment.recycler_org_id = ? AND batch.zone = ?", recyclerOrgID, zone).
		Order("assignment.assigned_at DESC, assignment.assignment_sequence DESC, assignment.id DESC").
		Limit(1).
		Scan(&candidate).Error
	if err != nil {
		return nil, err
	}
	if candidate.UserID == "" {
		return nil, nil
	}
	return &candidate, nil
}

// findNextActiveCollector locks the chosen scope and returns the first active
// collector after after in the stable organisation/user order, wrapping to the
// beginning. Excluding the prior user prevents immediate re-assignment after a
// rejection or failed pickup.
func findNextActiveCollector(ctx context.Context, db *gorm.DB, recyclerOrgID, zone string, after *CollectorCandidate, excludeUserID string, now time.Time) (*CollectorCandidate, error) {
	query := db.WithContext(ctx).
		Table("recycler_collector_scopes AS scope").
		Select("collector.user_id AS user_id, collector.organisation_id AS organisation_id, scope.id AS scope_id, scope.version AS scope_version").
		Joins("INNER JOIN users AS collector ON collector.organisation_id = scope.collector_org_id").
		Joins("INNER JOIN roles AS role ON role.role_code = collector.role_code").
		Joins("INNER JOIN organisations AS organisation ON organisation.organisation_id = scope.collector_org_id").
		Where(`scope.recycler_org_id = ? AND scope.zone = ? AND scope.is_active = TRUE AND scope.valid_from <= ? AND (scope.valid_until IS NULL OR scope.valid_until > ?)`, recyclerOrgID, zone, now, now).
		Where(`collector.status = 'ACTIVE' AND collector.role_code = 'COLLECTOR' AND role.is_active = TRUE AND role.allowed_organisation_type = 'COLLECTION_OPERATOR' AND organisation.organisation_type = 'COLLECTION_OPERATOR' AND organisation.status = 'ACTIVE'`)
	if excludeUserID != "" {
		query = query.Where("collector.user_id <> ?", excludeUserID)
	}
	if after != nil {
		query = query.Clauses(clause.OrderBy{Expression: clause.Expr{
			SQL:  "CASE WHEN scope.collector_org_id > ? OR (scope.collector_org_id = ? AND collector.user_id > ?) THEN 0 ELSE 1 END, scope.collector_org_id, collector.user_id, scope.id",
			Vars: []any{after.OrganisationID, after.OrganisationID, after.UserID},
		}})
	} else {
		query = query.Order("scope.collector_org_id, collector.user_id, scope.id")
	}
	query = query.Clauses(clause.Locking{Strength: "UPDATE"}).Limit(1)

	var candidate CollectorCandidate
	err := query.Scan(&candidate).Error
	if err != nil {
		return nil, err
	}
	if candidate.UserID == "" {
		return nil, ErrAssignmentNoActiveCollector
	}
	return &candidate, nil
}
