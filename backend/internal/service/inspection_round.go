package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/constants"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/dto"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/repository"
)

type InspectionRoundService interface {
	List(context.Context, dto.PageQuery) (repository.Page[model.InspectionRound], error)
	Get(context.Context, uint) (model.InspectionRound, error)
	Create(context.Context, dto.CreateInspectionRound, string, string) (model.InspectionRound, error)
	Update(context.Context, uint, dto.UpdateInspectionRound, string, string) (model.InspectionRound, error)
	Transition(context.Context, uint, dto.TransitionRequest, string, string) (model.InspectionRound, error)
	Delete(context.Context, uint, string, string) error
	StatusCounts(context.Context) (map[string]int64, error)
	LatestCompletionCheck(context.Context, string) (model.CompletionCheckView, bool, error)
}

type inspectionRoundService struct {
	repository repository.InspectionRoundRepository
	security   SecurityService
	gate       CompletionGateService
}

func NewInspectionRoundService(repo repository.InspectionRoundRepository, security SecurityService, gate CompletionGateService) InspectionRoundService {
	return &inspectionRoundService{repository: repo, security: security, gate: gate}
}

func (s *inspectionRoundService) List(ctx context.Context, query dto.PageQuery) (repository.Page[model.InspectionRound], error) {
	page, err := s.repository.List(ctx, query)
	if err != nil {
		return page, err
	}
	items, err := s.gate.EnrichRounds(ctx, page.Items)
	if err != nil {
		return page, err
	}
	page.Items = items
	return page, nil
}

func (s *inspectionRoundService) Get(ctx context.Context, id uint) (model.InspectionRound, error) {
	item, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.InspectionRound{}, err
	}
	items, err := s.gate.EnrichRounds(ctx, []model.InspectionRound{item})
	if err != nil {
		return model.InspectionRound{}, err
	}
	return items[0], nil
}

func (s *inspectionRoundService) Create(ctx context.Context, input dto.CreateInspectionRound, actor, requestID string) (model.InspectionRound, error) {
	if err := validateInspectionRoundBusinessFields(input.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.InspectionRound{}, err
	}
	item := model.InspectionRound{
		BaseModel: model.BaseModel{
			Code: strings.ToUpper(strings.TrimSpace(input.Code)), Name: strings.TrimSpace(input.Name),
			Status: model.InspectionRoundInitialStatus, Version: 1, Description: strings.TrimSpace(input.Description),
		},
		Facility: strings.TrimSpace(input.Facility), Owner: strings.TrimSpace(input.Owner),
		Category: strings.TrimSpace(input.Category), RiskLevel: input.RiskLevel,
		MetricValue: input.MetricValue, MetricUnit: strings.TrimSpace(input.MetricUnit),
		EffectiveAt: input.EffectiveAt.UTC(), Evidence: strings.TrimSpace(input.Evidence),
		RelatedCode: strings.ToUpper(strings.TrimSpace(input.RelatedCode)),
	}
	if err := s.repository.Create(ctx, &item); err != nil {
		return model.InspectionRound{}, fmt.Errorf("create 检查批次: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "create", "InspectionRound", item.ID, "", item.Status, "created 检查批次")
	return s.Get(ctx, item.ID)
}

func (s *inspectionRoundService) Update(ctx context.Context, id uint, input dto.UpdateInspectionRound, actor, requestID string) (model.InspectionRound, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.InspectionRound{}, err
	}
	if err := validateInspectionRoundBusinessFields(current.Code, input.Name, input.Facility, input.Owner); err != nil {
		return model.InspectionRound{}, err
	}
	current.Name = strings.TrimSpace(input.Name)
	current.Description = strings.TrimSpace(input.Description)
	current.Facility = strings.TrimSpace(input.Facility)
	current.Owner = strings.TrimSpace(input.Owner)
	current.Category = strings.TrimSpace(input.Category)
	current.RiskLevel = input.RiskLevel
	current.MetricValue = input.MetricValue
	current.MetricUnit = strings.TrimSpace(input.MetricUnit)
	current.EffectiveAt = input.EffectiveAt.UTC()
	current.Evidence = strings.TrimSpace(input.Evidence)
	current.RelatedCode = strings.ToUpper(strings.TrimSpace(input.RelatedCode))
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.repository.Update(ctx, id, input.ExpectedVersion, &current); err != nil {
		return model.InspectionRound{}, fmt.Errorf("update 检查批次: %w", err)
	}
	_ = s.security.Audit(ctx, actor, requestID, "update", "InspectionRound", id, current.Status, current.Status, "updated business fields")
	return s.Get(ctx, id)
}

func (s *inspectionRoundService) Transition(ctx context.Context, id uint, input dto.TransitionRequest, actor, requestID string) (model.InspectionRound, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return model.InspectionRound{}, err
	}
	target := strings.TrimSpace(input.Status)
	if !constants.CanTransition(constants.InspectionRoundTransitions, current.Status, target) {
		return model.InspectionRound{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current.Status, target)
	}

	// 批次从复核进入完成时，先执行缺陷处置核验。
	if target == "completed" {
		return s.completeWithVerification(ctx, current, input, actor, requestID)
	}

	before := current.Status
	current.Status = target
	current.Version = input.ExpectedVersion + 1
	current.UpdatedAt = time.Now().UTC()
	if err := s.repository.Update(ctx, id, input.ExpectedVersion, &current); err != nil {
		return model.InspectionRound{}, fmt.Errorf("transition 检查批次: %w", err)
	}
	if err := s.security.Audit(ctx, actor, requestID, "transition", "InspectionRound", id, before, target, input.Reason); err != nil {
		return model.InspectionRound{}, fmt.Errorf("persist transition audit: %w", err)
	}
	return s.Get(ctx, id)
}

// completeWithVerification enforces 缺陷处置核验 before review -> completed.
// On any blocker the round, defects, priority decisions and audit logs stay
// untouched; only an append-only blocked verification row is written.
func (s *inspectionRoundService) completeWithVerification(ctx context.Context, current model.InspectionRound, input dto.TransitionRequest, actor, requestID string) (model.InspectionRound, error) {
	details, blockers, err := s.gate.Verify(ctx, current)
	if err != nil {
		return model.InspectionRound{}, err
	}
	if len(blockers) > 0 {
		if _, err := s.gate.RecordBlocked(ctx, current, actor, requestID, details, blockers); err != nil {
			return model.InspectionRound{}, err
		}
		return model.InspectionRound{}, fmt.Errorf("%w: %s", ErrCompletionBlocked, strings.Join(blockerCodes(blockers), ","))
	}

	// The passing check and the optimistic-locked status update share one
	// transaction: a stale version changes nothing and inserts no check, so
	// duplicate/concurrent completions can succeed at most once.
	if err := s.gate.CompleteAndCheck(ctx, current, input.ExpectedVersion, actor, requestID, details); err != nil {
		return model.InspectionRound{}, fmt.Errorf("transition 检查批次: %w", err)
	}
	if err := s.security.Audit(ctx, actor, requestID, "transition", "InspectionRound", current.ID, current.Status, "completed", input.Reason); err != nil {
		return model.InspectionRound{}, fmt.Errorf("persist transition audit: %w", err)
	}
	return s.Get(ctx, current.ID)
}

func (s *inspectionRoundService) Delete(ctx context.Context, id uint, actor, requestID string) error {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repository.Delete(ctx, id); err != nil {
		return err
	}
	return s.security.Audit(ctx, actor, requestID, "delete", "InspectionRound", id, current.Status, "deleted", "soft deleted 检查批次")
}

func (s *inspectionRoundService) StatusCounts(ctx context.Context) (map[string]int64, error) {
	return s.repository.CountByStatus(ctx)
}

func (s *inspectionRoundService) LatestCompletionCheck(ctx context.Context, roundCode string) (model.CompletionCheckView, bool, error) {
	return s.gate.GetLatestByRound(ctx, roundCode)
}

func blockerCodes(blockers []model.CompletionDefectDetail) []string {
	codes := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		codes = append(codes, blocker.DefectCode)
	}
	return codes
}

func validateInspectionRoundBusinessFields(code, name, facility, owner string) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(facility) == "" || strings.TrimSpace(owner) == "" {
		return ErrInvalidInput
	}
	return nil
}
