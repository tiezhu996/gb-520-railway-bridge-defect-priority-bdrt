package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/constants"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/repository"
	"gorm.io/gorm"
)

// Disposed defect states accepted by the completion gate. New or verified
// defects must leave these open states before a batch can be completed.
var disposedDefectStates = map[string]bool{
	string(constants.DefectStateMonitoring): true,
	string(constants.DefectStateMitigated):  true,
	string(constants.DefectStateClosed):     true,
}

var severeRiskLevels = map[string]bool{"high": true, "critical": true}

// CompletionGateService implements 检查批次完成前的缺陷处置核验. It verifies all
// defects associated with a round (defect.relatedCode matches the round code or
// the round relatedCode), persists every attempt (passed or blocked) as an
// append-only check, and enriches read models so both workbenches can re-read
// the result after refresh.
type CompletionGateService interface {
	// Verify evaluates the associated defects without persisting anything.
	Verify(ctx context.Context, round model.InspectionRound) (model.CompletionCheckDetails, []model.CompletionDefectDetail, error)
	// RecordBlocked persists a blocked verification attempt. It never touches
	// rounds, defects, priority decisions or audit logs.
	RecordBlocked(ctx context.Context, round model.InspectionRound, actor, requestID string, details model.CompletionCheckDetails, blockers []model.CompletionDefectDetail) (model.CompletionCheckView, error)
	// CompleteAndCheck performs the optimistic-locked review->completed update
	// and inserts the passing check in one transaction.
	CompleteAndCheck(ctx context.Context, round model.InspectionRound, expectedVersion uint, actor, requestID string, details model.CompletionCheckDetails) error
	// GetLatestByRound returns the most recent verification for one round code.
	GetLatestByRound(ctx context.Context, roundCode string) (model.CompletionCheckView, bool, error)
	// EnrichRounds attaches latest verifications to round list responses.
	EnrichRounds(ctx context.Context, rounds []model.InspectionRound) ([]model.InspectionRound, error)
	// EnrichDefects attaches the latest passing verification covering each defect.
	EnrichDefects(ctx context.Context, defects []model.DefectFinding) ([]model.DefectFinding, error)
}

type completionGateService struct {
	rounds     repository.InspectionRoundRepository
	defects    repository.DefectFindingRepository
	priorities repository.PriorityDecisionRepository
	checks     repository.CompletionCheckRepository
	audits     repository.SecurityRepository
}

func NewCompletionGateService(
	rounds repository.InspectionRoundRepository,
	defects repository.DefectFindingRepository,
	priorities repository.PriorityDecisionRepository,
	checks repository.CompletionCheckRepository,
	audits repository.SecurityRepository,
) CompletionGateService {
	return &completionGateService{rounds: rounds, defects: defects, priorities: priorities, checks: checks, audits: audits}
}

func (g *completionGateService) Verify(ctx context.Context, round model.InspectionRound) (model.CompletionCheckDetails, []model.CompletionDefectDetail, error) {
	details := model.CompletionCheckDetails{RoundStatus: round.Status, Defects: make([]model.CompletionDefectDetail, 0)}
	blockers := make([]model.CompletionDefectDetail, 0)

	// A defect is associated with the round when defect.relatedCode references
	// either the round code (workbench convention) or the round relatedCode
	// (seeded work-order chain convention).
	keys := []string{strings.ToUpper(strings.TrimSpace(round.Code))}
	if related := strings.ToUpper(strings.TrimSpace(round.RelatedCode)); related != "" && related != keys[0] {
		keys = append(keys, related)
	}
	related, err := g.defects.ListByRelatedCodes(ctx, keys)
	if err != nil {
		return details, blockers, fmt.Errorf("load related defects: %w", err)
	}
	if len(related) == 0 {
		return details, blockers, nil
	}

	defectIDs := make([]uint, 0, len(related))
	defectCodes := make([]string, 0, len(related))
	for _, defect := range related {
		defectIDs = append(defectIDs, defect.ID)
		defectCodes = append(defectCodes, defect.Code)
	}
	transitionAudits, err := g.audits.DefectTransitionAudits(ctx, defectIDs)
	if err != nil {
		return details, blockers, fmt.Errorf("load defect transition audits: %w", err)
	}
	linkedPriorities, err := g.priorities.ListByRelatedCodes(ctx, defectCodes)
	if err != nil {
		return details, blockers, fmt.Errorf("load linked priorities: %w", err)
	}
	finalizedByDefect := make(map[string]model.PriorityDecision)
	for _, priority := range linkedPriorities {
		if priority.Status != "draft" {
			// keep the highest-priority (urgent > restrict > observe) finalized link
			current, exists := finalizedByDefect[priority.RelatedCode]
			if !exists || priorityRank(priority.Status) > priorityRank(current.Status) {
				finalizedByDefect[priority.RelatedCode] = priority
			}
		}
	}

	for _, defect := range related {
		detail := model.CompletionDefectDetail{
			DefectID: defect.ID, DefectCode: defect.Code, State: defect.Status,
			RiskLevel: defect.RiskLevel, Disposed: disposedDefectStates[defect.Status],
		}
		if audit, exists := transitionAudits[defect.ID]; exists && strings.TrimSpace(audit.Detail) != "" {
			detail.DispositionBasis = strings.TrimSpace(audit.Detail)
		}
		if priority, exists := finalizedByDefect[defect.Code]; exists {
			detail.PriorityCode = priority.Code
			detail.PriorityStatus = priority.Status
			// A finalized decision with its own evidence is itself an accepted basis.
			if detail.DispositionBasis == "" && strings.TrimSpace(priority.Evidence) != "" {
				detail.DispositionBasis = "已定稿优先级 " + priority.Code + " 证据：" + strings.TrimSpace(priority.Evidence)
			}
		}

		switch {
		case !detail.Disposed:
			detail.BlockerReason = model.CompletionBlockPendingDisposition
			detail.BlockerMessage = "缺陷仍为 " + defect.Status + "，须转为监测、缓解或关闭并写明依据"
		case detail.DispositionBasis == "":
			detail.BlockerReason = model.CompletionBlockMissingBasis
			detail.BlockerMessage = "缺陷已进入 " + defect.Status + "，但缺少处置依据（迁移说明或已定稿优先级证据）"
		case severeRiskLevels[defect.RiskLevel] && detail.PriorityCode == "":
			detail.BlockerReason = model.CompletionBlockMissingPriority
			detail.BlockerMessage = "高风险/关键风险缺陷须关联已定稿的处置优先级（观察/限速/立即处置）"
		}

		details.Defects = append(details.Defects, detail)
		if detail.BlockerReason != "" {
			blockers = append(blockers, detail)
		}
	}
	sort.SliceStable(details.Defects, func(i, j int) bool { return details.Defects[i].DefectCode < details.Defects[j].DefectCode })
	return details, blockers, nil
}

func (g *completionGateService) RecordBlocked(ctx context.Context, round model.InspectionRound, actor, requestID string, details model.CompletionCheckDetails, blockers []model.CompletionDefectDetail) (model.CompletionCheckView, error) {
	raw, err := json.Marshal(details)
	if err != nil {
		return model.CompletionCheckView{}, fmt.Errorf("serialize blocked completion check: %w", err)
	}
	codes := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		codes = append(codes, blocker.DefectCode)
	}
	check := model.InspectionCompletionCheck{
		InspectionRoundID:  round.ID,
		RoundCode:          round.Code,
		Status:             model.CompletionCheckBlocked,
		Actor:              defaultActor(actor),
		RequestID:          defaultRequestID(requestID),
		RelatedDefectCount: len(details.Defects),
		BlockerCodes:       strings.Join(codes, ","),
		Details:            string(raw),
		CreatedAt:          time.Now().UTC(),
	}
	if err := g.checks.Create(ctx, &check); err != nil {
		return model.CompletionCheckView{}, fmt.Errorf("persist blocked completion check: %w", err)
	}
	return toCheckView(check, details), nil
}

func (g *completionGateService) CompleteAndCheck(ctx context.Context, round model.InspectionRound, expectedVersion uint, actor, requestID string, details model.CompletionCheckDetails) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("serialize passing completion check: %w", err)
	}
	updated := round
	updated.LatestCompletionCheck = nil
	updated.Status = "completed"
	updated.Version = expectedVersion + 1
	updated.UpdatedAt = time.Now().UTC()
	check := model.InspectionCompletionCheck{
		InspectionRoundID:  round.ID,
		RoundCode:          round.Code,
		Status:             model.CompletionCheckPassed,
		Actor:              defaultActor(actor),
		RequestID:          defaultRequestID(requestID),
		RelatedDefectCount: len(details.Defects),
		BlockerCodes:       "",
		Details:            string(raw),
		CreatedAt:          time.Now().UTC(),
	}
	if err := g.rounds.CompleteWithCheck(ctx, round.ID, expectedVersion, &updated, &check); err != nil {
		return err
	}
	return nil
}

func (g *completionGateService) GetLatestByRound(ctx context.Context, roundCode string) (model.CompletionCheckView, bool, error) {
	check, err := g.checks.LatestByRoundCode(ctx, strings.ToUpper(strings.TrimSpace(roundCode)))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CompletionCheckView{}, false, nil
	}
	if err != nil {
		return model.CompletionCheckView{}, false, err
	}
	details, err := model.ParseCheckDetails(check.Details)
	if err != nil {
		return model.CompletionCheckView{}, false, err
	}
	return toCheckView(check, details), true, nil
}

func (g *completionGateService) EnrichRounds(ctx context.Context, rounds []model.InspectionRound) ([]model.InspectionRound, error) {
	if len(rounds) == 0 {
		return rounds, nil
	}
	ids := make([]uint, 0, len(rounds))
	for index := range rounds {
		ids = append(ids, rounds[index].ID)
	}
	latest, err := g.checks.LatestByRoundIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for index := range rounds {
		if check, exists := latest[rounds[index].ID]; exists {
			details, err := model.ParseCheckDetails(check.Details)
			if err != nil {
				return nil, err
			}
			view := toCheckView(check, details)
			rounds[index].LatestCompletionCheck = &view
		}
	}
	return rounds, nil
}

func (g *completionGateService) EnrichDefects(ctx context.Context, defects []model.DefectFinding) ([]model.DefectFinding, error) {
	if len(defects) == 0 {
		return defects, nil
	}
	refs, err := g.rounds.ListCodeRefs(ctx)
	if err != nil {
		return nil, fmt.Errorf("load round code refs: %w", err)
	}
	codes := make([]string, 0, len(defects))
	roundCodesByDefect := make(map[string][]string)
	seenRoundCodes := make(map[string]bool)
	roundCodes := make([]string, 0)
	for index := range defects {
		codes = append(codes, defects[index].Code)
		defectRef := strings.ToUpper(strings.TrimSpace(defects[index].RelatedCode))
		if defectRef == "" {
			continue
		}
		for _, ref := range refs {
			if ref.Code != defectRef && strings.ToUpper(strings.TrimSpace(ref.RelatedCode)) != defectRef {
				continue
			}
			roundCodesByDefect[defects[index].Code] = append(roundCodesByDefect[defects[index].Code], ref.Code)
			if !seenRoundCodes[ref.Code] {
				seenRoundCodes[ref.Code] = true
				roundCodes = append(roundCodes, ref.Code)
			}
		}
	}
	latestByRound, err := g.checks.LatestByRoundCodes(ctx, roundCodes)
	if err != nil {
		return nil, err
	}
	passingByDefect, err := g.checks.LatestPassedByDefectCodes(ctx, codes)
	if err != nil {
		return nil, err
	}
	for index := range defects {
		var check model.InspectionCompletionCheck
		found := false
		// Prefer the latest verification of any round this defect currently
		// references, so blocked attempts with blocker reasons are visible too.
		for _, roundCode := range roundCodesByDefect[defects[index].Code] {
			latest, exists := latestByRound[roundCode]
			if !exists || (found && latest.ID <= check.ID) {
				continue
			}
			details, err := model.ParseCheckDetails(latest.Details)
			if err != nil {
				return nil, err
			}
			if _, ok := findDefectDetail(details, defects[index].Code); ok {
				check, found = latest, true
			}
		}
		// Fall back to the most recent passing verification that covered the
		// defect (e.g. defects whose relatedCode no longer points at the round).
		if !found {
			if latest, exists := passingByDefect[defects[index].Code]; exists {
				check, found = latest, true
			}
		}
		if !found {
			continue
		}
		details, err := model.ParseCheckDetails(check.Details)
		if err != nil {
			return nil, err
		}
		defectDetail, ok := findDefectDetail(details, defects[index].Code)
		if !ok {
			continue
		}
		defects[index].CompletionCheck = &model.DefectCompletionCheckView{
			InspectionRoundID: check.InspectionRoundID,
			RoundCode:         check.RoundCode,
			Status:            check.Status,
			Actor:             check.Actor,
			RequestID:         check.RequestID,
			Defect:            defectDetail,
			CreatedAt:         check.CreatedAt,
		}
	}
	return defects, nil
}

func findDefectDetail(details model.CompletionCheckDetails, defectCode string) (model.CompletionDefectDetail, bool) {
	for _, defect := range details.Defects {
		if defect.DefectCode == defectCode {
			return defect, true
		}
	}
	return model.CompletionDefectDetail{}, false
}

func toCheckView(check model.InspectionCompletionCheck, details model.CompletionCheckDetails) model.CompletionCheckView {
	codes := make([]string, 0)
	for _, part := range strings.Split(check.BlockerCodes, ",") {
		if part = strings.TrimSpace(part); part != "" {
			codes = append(codes, part)
		}
	}
	return model.CompletionCheckView{
		InspectionRoundID:  check.InspectionRoundID,
		RoundCode:          check.RoundCode,
		Status:             check.Status,
		Actor:              check.Actor,
		RequestID:          check.RequestID,
		RelatedDefectCount: check.RelatedDefectCount,
		BlockerCodes:       codes,
		Details:            details,
		CreatedAt:          check.CreatedAt,
	}
}

func priorityRank(status string) int {
	switch status {
	case "urgent":
		return 3
	case "restrict":
		return 2
	case "observe":
		return 1
	default:
		return 0
	}
}

func defaultActor(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return "system"
	}
	return strings.TrimSpace(actor)
}

func defaultRequestID(requestID string) string {
	if strings.TrimSpace(requestID) == "" {
		return "untracked"
	}
	return strings.TrimSpace(requestID)
}
