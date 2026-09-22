package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/constants"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/dto"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/repository"
	"gorm.io/gorm"
)

type InspectionRoundService interface {
	List(context.Context, dto.PageQuery) (repository.Page[model.InspectionRound], error)
	Get(context.Context, uint) (model.InspectionRound, error)
	Create(context.Context, dto.CreateInspectionRound, string, string) (model.InspectionRound, error)
	Update(context.Context, uint, dto.UpdateInspectionRound, string, string) (model.InspectionRound, error)
	Transition(context.Context, uint, dto.TransitionRequest, string, string) (model.InspectionRound, error)
	Delete(context.Context, uint, string, string) error
	StatusCounts(context.Context) (map[string]int64, error)
	// CompletionChecks returns the latest 缺陷处置核验 result for every round.
	CompletionChecks(context.Context) ([]model.InspectionCompletionCheck, error)
	// CompletionCheckForRound returns the verification result stored for one round.
	CompletionCheckForRound(context.Context, uint) (model.InspectionCompletionCheck, error)
}

type inspectionRoundService struct {
	repository repository.InspectionRoundRepository
	checks     repository.CompletionCheckRepository
	security   SecurityService
}

func NewInspectionRoundService(repo repository.InspectionRoundRepository, checks repository.CompletionCheckRepository, security SecurityService) InspectionRoundService {
	return &inspectionRoundService{repository: repo, checks: checks, security: security}
}

func (s *inspectionRoundService) List(ctx context.Context, query dto.PageQuery) (repository.Page[model.InspectionRound], error) {
	return s.repository.List(ctx, query)
}

func (s *inspectionRoundService) Get(ctx context.Context, id uint) (model.InspectionRound, error) {
	return s.repository.Get(ctx, id)
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
	return item, nil
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
	return s.repository.Get(ctx, id)
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
	// Leaving review for completion requires the 缺陷处置核验 gate. Every other
	// transition keeps the original optimistic-lock semantics.
	if current.Status == "review" && target == "completed" {
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
	return s.repository.Get(ctx, id)
}

// completeWithVerification enforces the completion gate. The verification
// outcome is always persisted (readable after refresh). On failure nothing
// else is written: the round keeps status/version, defects and decisions are
// untouched, and no audit row is appended. On success the round update and
// the passed check commit in one transaction; the optimistic version predicate
// makes duplicate/concurrent completions succeed exactly once.
func (s *inspectionRoundService) completeWithVerification(ctx context.Context, round model.InspectionRound, input dto.TransitionRequest, actor, requestID string) (model.InspectionRound, error) {
	var blockers []model.CompletionBlocker
	var checkedIDs []uint
	var lockedRound model.InspectionRound

	txErr := s.repository.InTransaction(ctx, func(tx *gorm.DB) error {
		blockers = nil
		checkedIDs = nil

		// Re-read inside the transaction so a concurrent completion or reopen
		// is visible before we verify.
		locked, err := s.repository.GetTx(ctx, tx, round.ID)
		if err != nil {
			return err
		}
		lockedRound = locked
		if locked.Status != "review" {
			// A duplicate/concurrent completion already moved the round: let
			// the optimistic predicate / state check reject this attempt.
			return repository.ErrVersionConflict
		}
		linked, err := s.checks.DefectsForRoundTx(ctx, tx, locked.Code)
		if err != nil {
			return err
		}
		criticalCodes := make([]string, 0)
		for _, defect := range linked {
			checkedIDs = append(checkedIDs, defect.ID)
			switch defect.Status {
			case string(constants.DefectStateNew), string(constants.DefectStateVerified):
				blockers = append(blockers, model.CompletionBlocker{
					DefectID: defect.ID, DefectCode: defect.Code, Status: defect.Status,
					RiskLevel: defect.RiskLevel, Reason: "新发现或已核实缺陷尚未转为监测、缓解或关闭",
					Code: model.BlockerUndisposed,
				})
			case string(constants.DefectStateMonitoring), string(constants.DefectStateMitigated), string(constants.DefectStateClosed):
				if strings.TrimSpace(defect.DispositionBasis) == "" {
					blockers = append(blockers, model.CompletionBlocker{
						DefectID: defect.ID, DefectCode: defect.Code, Status: defect.Status,
						RiskLevel: defect.RiskLevel, Reason: "缺陷已处置但缺少处置依据",
						Code: model.BlockerMissingBasis,
					})
				}
			}
			if defect.RiskLevel == "high" || defect.RiskLevel == "critical" {
				criticalCodes = append(criticalCodes, defect.Code)
			}
		}

		finalized, err := s.checks.FinalizedPrioritiesForCodesTx(ctx, tx, criticalCodes)
		if err != nil {
			return err
		}
		covered := make(map[string]bool, len(finalized))
		for _, decision := range finalized {
			covered[strings.ToUpper(strings.TrimSpace(decision.RelatedCode))] = true
		}
		for _, defect := range linked {
			if (defect.RiskLevel == "high" || defect.RiskLevel == "critical") && !covered[strings.ToUpper(strings.TrimSpace(defect.Code))] {
				blockers = append(blockers, model.CompletionBlocker{
					DefectID: defect.ID, DefectCode: defect.Code, Status: defect.Status,
					RiskLevel: defect.RiskLevel, Reason: "严重或关键风险缺陷缺少已定稿的处置优先级",
					Code: model.BlockerMissingPriority,
				})
			}
		}

		if len(blockers) > 0 {
			return nil // evaluated; persistence of the blocked outcome happens outside
		}

		locked.Status = "completed"
		locked.Version = input.ExpectedVersion + 1
		locked.UpdatedAt = time.Now().UTC()
		check := buildCompletionCheck(locked, checkedIDs, nil, actor, requestID, true)
		if err := s.repository.CompleteConditional(ctx, tx, locked.ID, input.ExpectedVersion, &locked, &check); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		if errors.Is(txErr, repository.ErrVersionConflict) {
			return model.InspectionRound{}, fmt.Errorf("complete 检查批次: %w", txErr)
		}
		return model.InspectionRound{}, fmt.Errorf("verify 检查批次 completion: %w", txErr)
	}

	check := buildCompletionCheck(lockedRound, checkedIDs, blockers, actor, requestID, len(blockers) == 0)
	if len(blockers) > 0 {
		// Persist the blocked outcome in its own transaction. The batch,
		// defects, decisions and audit log are not modified on failure.
		_ = s.checks.Upsert(ctx, &check)
		seen := make(map[string]bool, len(blockers))
		codes := make([]string, 0, len(blockers))
		for _, blocker := range blockers {
			if !seen[blocker.DefectCode] {
				seen[blocker.DefectCode] = true
				codes = append(codes, blocker.DefectCode)
			}
		}
		return model.InspectionRound{}, &ErrCompletionBlocked{
			Message: fmt.Sprintf("检查批次完成核验未通过，阻塞缺陷: %s", strings.Join(codes, ", ")),
			Blockers: blockers,
		}
	}

	if err := s.security.Audit(ctx, actor, requestID, "transition", "InspectionRound", round.ID, "review", "completed",
		fmt.Sprintf("%s; completion verification passed (%d defects checked)", strings.TrimSpace(input.Reason), len(checkedIDs))); err != nil {
		return model.InspectionRound{}, fmt.Errorf("persist transition audit: %w", err)
	}
	return s.repository.Get(ctx, round.ID)
}

func buildCompletionCheck(round model.InspectionRound, defectIDs []uint, blockers []model.CompletionBlocker, actor, requestID string, passed bool) model.InspectionCompletionCheck {
	ids := make([]string, 0, len(defectIDs))
	for _, id := range defectIDs {
		ids = append(ids, strconv.FormatUint(uint64(id), 10))
	}
	blockerJSON := "[]"
	if len(blockers) > 0 {
		if raw, err := json.Marshal(blockers); err == nil {
			blockerJSON = string(raw)
		}
	}
	return model.InspectionCompletionCheck{
		InspectionRoundID: round.ID, RoundCode: round.Code, Passed: passed,
		DefectTotal: len(defectIDs), CheckedDefectIDs: strings.Join(ids, ","),
		Blockers: blockerJSON, Actor: actor, RequestID: requestID, CheckedAt: time.Now().UTC(),
	}
}

func (s *inspectionRoundService) CompletionChecks(ctx context.Context) ([]model.InspectionCompletionCheck, error) {
	return s.checks.List(ctx)
}

func (s *inspectionRoundService) CompletionCheckForRound(ctx context.Context, roundID uint) (model.InspectionCompletionCheck, error) {
	return s.checks.GetByRound(ctx, roundID)
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

func validateInspectionRoundBusinessFields(code, name, facility, owner string) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(facility) == "" || strings.TrimSpace(owner) == "" {
		return ErrInvalidInput
	}
	return nil
}
