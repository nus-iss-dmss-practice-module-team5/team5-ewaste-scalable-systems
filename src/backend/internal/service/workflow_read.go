package service

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"workflow-api/internal/dto"
	"workflow-api/internal/model"
	"workflow-api/internal/repository"
)

var (
	ErrWorkflowReadForbidden = errors.New("workflow read: forbidden")
	ErrWorkflowReadNotFound  = errors.New("workflow read: not found")
	ErrWorkflowReadInvalid   = errors.New("workflow read: invalid request")
)

type WorkflowReadActor struct {
	UserID         string
	OrganisationID string
	RoleCode       string
}

type WorkflowReadPage struct {
	Page     int
	PageSize int
}

type BatchListResult struct {
	Data       []dto.BatchView
	Page       int
	PageSize   int
	TotalCount int64
}

type ProcessingListResult struct {
	Data       []dto.ProcessingSummaryView
	Page       int
	PageSize   int
	TotalCount int64
}

type OpportunityListResult struct {
	Data       []dto.OpportunityView
	Page       int
	PageSize   int
	TotalCount int64
}

type AssignmentListResult struct {
	Data       []dto.AssignmentView
	Page       int
	PageSize   int
	TotalCount int64
}

type WorkflowReadService struct {
	repository repository.WorkflowReadRepository
}

func NewWorkflowReadService(repo repository.WorkflowReadRepository) *WorkflowReadService {
	return &WorkflowReadService{repository: repo}
}

func (s *WorkflowReadService) ListBatches(
	ctx context.Context,
	actor WorkflowReadActor,
	status string,
	page WorkflowReadPage,
) (BatchListResult, error) {
	if err := validatePage(page); err != nil {
		return BatchListResult{}, err
	}
	if !isRole(actor.RoleCode, "DONOR", "COLLECTOR") {
		return BatchListResult{}, ErrWorkflowReadForbidden
	}

	var batchStatus *model.BatchStatus
	if strings.TrimSpace(status) != "" {
		parsed, ok := parseBatchStatus(status)
		if !ok {
			return BatchListResult{}, ErrWorkflowReadInvalid
		}
		batchStatus = &parsed
	}

	batches, total, err := s.repository.ListBatches(ctx, toRepositoryScope(actor), batchStatus, toRepositoryPage(page))
	if err != nil {
		return BatchListResult{}, mapWorkflowReadRepositoryError(err)
	}
	result := BatchListResult{Page: page.Page, PageSize: page.PageSize, TotalCount: total, Data: make([]dto.BatchView, 0, len(batches))}
	for index := range batches {
		result.Data = append(result.Data, batchToDTO(&batches[index]))
	}
	return result, nil
}

func (s *WorkflowReadService) GetBatch(
	ctx context.Context,
	batchID string,
	actor WorkflowReadActor,
) (dto.BatchView, error) {
	if strings.TrimSpace(batchID) == "" {
		return dto.BatchView{}, ErrWorkflowReadInvalid
	}
	if !isRole(actor.RoleCode, "DONOR", "COLLECTOR") {
		return dto.BatchView{}, ErrWorkflowReadForbidden
	}

	batch, err := s.repository.FindBatch(ctx, batchID, toRepositoryScope(actor))
	if err != nil {
		return dto.BatchView{}, mapWorkflowReadRepositoryError(err)
	}
	return batchToDTO(batch), nil
}

func (s *WorkflowReadService) ListProcessingBatches(
	ctx context.Context,
	actor WorkflowReadActor,
	status string,
	page WorkflowReadPage,
) (ProcessingListResult, error) {
	if err := validatePage(page); err != nil {
		return ProcessingListResult{}, err
	}
	if !isRole(actor.RoleCode, "RECYCLER") {
		return ProcessingListResult{}, ErrWorkflowReadForbidden
	}

	var batchStatus *model.BatchStatus
	if strings.TrimSpace(status) != "" {
		parsed, ok := parseProcessingStatus(status)
		if !ok {
			return ProcessingListResult{}, ErrWorkflowReadInvalid
		}
		batchStatus = &parsed
	}

	summaries, total, err := s.repository.ListProcessingBatches(ctx, toRepositoryScope(actor), batchStatus, toRepositoryPage(page))
	if err != nil {
		return ProcessingListResult{}, mapWorkflowReadRepositoryError(err)
	}
	result := ProcessingListResult{
		Page: page.Page, PageSize: page.PageSize, TotalCount: total,
		Data: make([]dto.ProcessingSummaryView, 0, len(summaries)),
	}
	for index := range summaries {
		result.Data = append(result.Data, processingSummaryToDTO(&summaries[index]))
	}
	return result, nil
}

func (s *WorkflowReadService) GetProcessingBatch(
	ctx context.Context,
	batchID string,
	actor WorkflowReadActor,
) (dto.ProcessingDetailView, error) {
	if strings.TrimSpace(batchID) == "" {
		return dto.ProcessingDetailView{}, ErrWorkflowReadInvalid
	}
	if !isRole(actor.RoleCode, "RECYCLER") {
		return dto.ProcessingDetailView{}, ErrWorkflowReadForbidden
	}

	detail, err := s.repository.FindProcessingBatch(ctx, batchID, toRepositoryScope(actor))
	if err != nil {
		return dto.ProcessingDetailView{}, mapWorkflowReadRepositoryError(err)
	}
	return processingDetailToDTO(detail), nil
}

func (s *WorkflowReadService) ListOpportunities(
	ctx context.Context,
	actor WorkflowReadActor,
	page WorkflowReadPage,
) (OpportunityListResult, error) {
	if err := validatePage(page); err != nil {
		return OpportunityListResult{}, err
	}
	organisationID, err := s.repository.FindRecyclerOrganisation(ctx, actor.UserID)
	if err != nil {
		return OpportunityListResult{}, mapWorkflowReadRepositoryError(err)
	}

	opportunities, total, err := s.repository.ListOpportunities(ctx, organisationID, toRepositoryPage(page))
	if err != nil {
		return OpportunityListResult{}, mapWorkflowReadRepositoryError(err)
	}
	result := OpportunityListResult{Page: page.Page, PageSize: page.PageSize, TotalCount: total, Data: make([]dto.OpportunityView, 0, len(opportunities))}
	for index := range opportunities {
		result.Data = append(result.Data, opportunityToDTO(&opportunities[index]))
	}
	return result, nil
}

func (s *WorkflowReadService) GetOpportunity(
	ctx context.Context,
	batchID string,
	actor WorkflowReadActor,
) (dto.OpportunityView, error) {
	if strings.TrimSpace(batchID) == "" {
		return dto.OpportunityView{}, ErrWorkflowReadInvalid
	}
	organisationID, err := s.repository.FindRecyclerOrganisation(ctx, actor.UserID)
	if err != nil {
		return dto.OpportunityView{}, mapWorkflowReadRepositoryError(err)
	}

	opportunity, err := s.repository.FindOpportunity(ctx, batchID, organisationID)
	if err != nil {
		return dto.OpportunityView{}, mapWorkflowReadRepositoryError(err)
	}
	return opportunityToDTO(opportunity), nil
}

func (s *WorkflowReadService) ListAssignments(
	ctx context.Context,
	actor WorkflowReadActor,
	status string,
	page WorkflowReadPage,
) (AssignmentListResult, error) {
	if err := validatePage(page); err != nil {
		return AssignmentListResult{}, err
	}
	if !isRole(actor.RoleCode, "COLLECTOR") {
		return AssignmentListResult{}, ErrWorkflowReadForbidden
	}
	if strings.TrimSpace(status) != "" && !isAssignmentStatus(status) {
		return AssignmentListResult{}, ErrWorkflowReadInvalid
	}
	status = strings.ToUpper(strings.TrimSpace(status))

	assignments, total, err := s.repository.ListAssignments(ctx, actor.UserID, actor.OrganisationID, status, toRepositoryPage(page))
	if err != nil {
		return AssignmentListResult{}, mapWorkflowReadRepositoryError(err)
	}
	result := AssignmentListResult{Page: page.Page, PageSize: page.PageSize, TotalCount: total, Data: make([]dto.AssignmentView, 0, len(assignments))}
	for index := range assignments {
		result.Data = append(result.Data, assignmentToDTO(&assignments[index]))
	}
	return result, nil
}

func (s *WorkflowReadService) GetAssignment(
	ctx context.Context,
	assignmentID string,
	actor WorkflowReadActor,
) (dto.AssignmentView, error) {
	if strings.TrimSpace(assignmentID) == "" {
		return dto.AssignmentView{}, ErrWorkflowReadInvalid
	}
	if !isRole(actor.RoleCode, "COLLECTOR") {
		return dto.AssignmentView{}, ErrWorkflowReadForbidden
	}

	assignment, err := s.repository.FindAssignment(ctx, assignmentID, actor.UserID, actor.OrganisationID)
	if err != nil {
		return dto.AssignmentView{}, mapWorkflowReadRepositoryError(err)
	}
	return assignmentToDTO(assignment), nil
}

func validatePage(page WorkflowReadPage) error {
	if page.Page < 1 || page.PageSize < 1 || page.PageSize > 100 {
		return ErrWorkflowReadInvalid
	}
	return nil
}

func parseBatchStatus(value string) (model.BatchStatus, bool) {
	status := model.BatchStatus(strings.ToUpper(strings.TrimSpace(value)))
	switch status {
	case model.BatchStatusDraft, model.BatchStatusSubmitted, model.BatchStatusMatched, model.BatchStatusApproved, model.BatchStatusAssigned, model.BatchStatusCollected, model.BatchStatusVerified, model.BatchStatusRecycled, model.BatchStatusCompleted, model.BatchStatusFailedCollection:
		return status, true
	default:
		return "", false
	}
}

func parseProcessingStatus(value string) (model.BatchStatus, bool) {
	status := model.BatchStatus(strings.ToUpper(strings.TrimSpace(value)))
	switch status {
	case model.BatchStatusCollected, model.BatchStatusVerified, model.BatchStatusRecycled, model.BatchStatusCompleted:
		return status, true
	default:
		return "", false
	}
}

func isAssignmentStatus(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case model.AssignmentStatusAccepted, model.AssignmentStatusCompleted, model.AssignmentStatusFailed, model.AssignmentStatusSuperseded:
		return true
	default:
		return false
	}
}

func isRole(role string, allowed ...string) bool {
	role = strings.ToUpper(strings.TrimSpace(role))
	for _, candidate := range allowed {
		if role == candidate {
			return true
		}
	}
	return false
}

func toRepositoryScope(actor WorkflowReadActor) repository.WorkflowReadScope {
	return repository.WorkflowReadScope{UserID: actor.UserID, OrganisationID: actor.OrganisationID, RoleCode: actor.RoleCode}
}

func toRepositoryPage(page WorkflowReadPage) repository.WorkflowReadPage {
	return repository.WorkflowReadPage{Page: page.Page, PageSize: page.PageSize}
}

func opportunityToDTO(opportunity *model.WorkflowOpportunity) dto.OpportunityView {
	var estimatedWeightKg *float64
	if opportunity.EstimatedWeightKg != nil {
		if value, err := strconv.ParseFloat(*opportunity.EstimatedWeightKg, 64); err == nil {
			estimatedWeightKg = &value
		}
	}
	return dto.OpportunityView{
		BatchID: opportunity.BatchID, Status: string(opportunity.Status), Category: opportunity.Category,
		Version: int64(opportunity.Version), ClaimEpoch: strconv.FormatUint(opportunity.ClaimEpoch, 10),
		Quantity: opportunity.Quantity, EstimatedWeightKg: estimatedWeightKg, Zone: opportunity.Zone,
		CollectionDeadline: opportunity.CollectionDeadline, EligibilityReason: opportunity.EligibilityReason,
	}
}

func assignmentToDTO(assignment *model.BatchAssignment) dto.AssignmentView {
	return dto.AssignmentView{
		AssignmentID: assignment.ID, BatchID: assignment.BatchID, ClaimID: assignment.ClaimID,
		CollectorUserID: assignment.CollectorUserID, CollectorScopeID: assignment.CollectorScopeID,
		AssignmentStatus: assignment.AssignmentStatus, AssignmentSequence: int64(assignment.AssignmentSequence),
		Version: int64(assignment.Version), BatchVersion: int64(assignment.BatchVersion), CreatedAt: assignment.CreatedAt, UpdatedAt: assignment.UpdatedAt,
	}
}

func processingSummaryToDTO(summary *model.ProcessingSummary) dto.ProcessingSummaryView {
	return dto.ProcessingSummaryView{
		BatchID: summary.BatchID, Status: string(summary.Status),
		Version: int64(summary.Version), EvidenceStatus: summary.EvidenceStatus,
	}
}

func processingDetailToDTO(detail *model.ProcessingDetail) dto.ProcessingDetailView {
	return dto.ProcessingDetailView{
		BatchID:           detail.BatchID,
		Status:            string(detail.Status),
		Version:           int64(detail.Version),
		DeclaredCategory:  detail.DeclaredCategory,
		DeclaredQuantity:  detail.DeclaredQuantity,
		EstimatedWeightKg: detail.EstimatedWeightKg,
		ActualCategory:    detail.ActualCategory,
		ActualItemCount:   detail.ActualItemCount,
		ActualWeightKg:    detail.ActualWeightKg,
		ReusedKg:          detail.ReusedKg,
		RecycledKg:        detail.RecycledKg,
		DisposedKg:        detail.DisposedKg,
		UnknownKg:         detail.UnknownKg,
		DivertedKg:        detail.DivertedKg,
		DataQuality:       detail.DataQuality,
		EvidenceStatus:    detail.EvidenceStatus,
		AnomalyCodes:      append([]string{}, detail.AnomalyCodes...),
	}
}

func mapWorkflowReadRepositoryError(err error) error {
	switch {
	case errors.Is(err, repository.ErrWorkflowReadNotFound):
		return ErrWorkflowReadNotFound
	case errors.Is(err, repository.ErrWorkflowReadForbidden):
		return ErrWorkflowReadForbidden
	default:
		return err
	}
}
