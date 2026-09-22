package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/config"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/dto"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/repository"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type completionFixture struct {
	db        *gorm.DB
	gate      CompletionGateService
	rounds    InspectionRoundService
	defects   DefectFindingService
	roundRepo repository.InspectionRoundRepository
	checkRepo repository.CompletionCheckRepository
	auditRepo repository.SecurityRepository
}

func newCompletionFixture(t *testing.T) completionFixture {
	t.Helper()
	dsn := fmt.Sprintf("file:completion-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// One connection serializes writes so concurrent completion attempts race on
	// the optimistic-lock predicate exactly like PostgreSQL row locks.
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&model.User{}, &model.AuditLog{},
		&model.BridgeAsset{}, &model.InspectionRound{}, &model.DefectFinding{},
		&model.PriorityDecision{}, &model.PriorityDecisionRevision{},
		&model.InspectionCompletionCheck{},
	); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	securityRepo := repository.NewSecurityRepository(db)
	roundRepo := repository.NewInspectionRoundRepository(db)
	defectRepo := repository.NewDefectFindingRepository(db)
	priorityRepo := repository.NewPriorityDecisionRepository(db)
	checkRepo := repository.NewCompletionCheckRepository(db)
	securitySvc := NewSecurityService(securityRepo, config.Config{})
	gate := NewCompletionGateService(roundRepo, defectRepo, priorityRepo, checkRepo, securityRepo)
	return completionFixture{
		db: db, gate: gate,
		rounds:    NewInspectionRoundService(roundRepo, securitySvc, gate),
		defects:   NewDefectFindingService(defectRepo, securitySvc, gate),
		roundRepo: roundRepo, checkRepo: checkRepo, auditRepo: securityRepo,
	}
}

func (f completionFixture) createReviewRound(t *testing.T, code string) model.InspectionRound {
	t.Helper()
	round := model.InspectionRound{
		BaseModel: model.BaseModel{
			Code: code, Name: "待完成检查批次", Status: "review", Version: 1,
		},
		Facility: "K42 桥梁", Owner: "运行组", Category: "结构", RiskLevel: "high",
		EffectiveAt: time.Now().UTC(), Evidence: "批次复核证据",
	}
	if err := f.roundRepo.Create(context.Background(), &round); err != nil {
		t.Fatalf("create review round: %v", err)
	}
	return round
}

func (f completionFixture) createDefect(t *testing.T, code, roundCode, status, riskLevel string, withTransitionAudit bool) model.DefectFinding {
	t.Helper()
	ctx := context.Background()
	input := dto.CreateDefectFinding{
		Code: code, Name: "关联缺陷 " + code, Facility: "K42 桥梁", Owner: "处置组",
		Category: "结构", RiskLevel: riskLevel, MetricValue: 60, MetricUnit: "score",
		EffectiveAt: time.Now().UTC(), Evidence: "裂缝量测记录", RelatedCode: roundCode,
	}
	if status == "new" {
		created, err := f.defects.Create(ctx, input, "operator", "req-defect-create")
		if err != nil {
			t.Fatalf("create defect: %v", err)
		}
		return created
	}
	if !withTransitionAudit {
		// Seeded disposed state without any transition audit: the defect moved
		// into monitoring/mitigated/closed without a recorded basis.
		direct := model.DefectFinding{
			BaseModel: model.BaseModel{Code: code, Name: "关联缺陷 " + code, Status: status, Version: 1},
			Facility: "K42 桥梁", Owner: "处置组", Category: "结构", RiskLevel: riskLevel,
			EffectiveAt: time.Now().UTC(), Evidence: "裂缝量测记录", RelatedCode: roundCode,
		}
		if err := f.db.Create(&direct).Error; err != nil {
			t.Fatalf("seed direct defect: %v", err)
		}
		return direct
	}
	created, err := f.defects.Create(ctx, input, "operator", "req-defect-create")
	if err != nil {
		t.Fatalf("create defect: %v", err)
	}
	switch status {
	case "verified":
		return f.transitionDefect(t, created, "verified", "现场复核确认缺陷存在")
	case "monitoring":
		return f.transitionDefect(t, created, "monitoring", "纳入周期监测并设置阈值报警")
	case "mitigated":
		verified := f.transitionDefect(t, created, "verified", "现场复核确认缺陷存在")
		return f.transitionDefect(t, verified, "mitigated", "已完成临时加固并复测达标")
	case "closed":
		verified := f.transitionDefect(t, created, "verified", "现场复核确认缺陷存在")
		mitigated := f.transitionDefect(t, verified, "mitigated", "已完成临时加固并复测达标")
		return f.transitionDefect(t, mitigated, "closed", "专项整治验收合格关闭")
	default:
		t.Fatalf("unsupported defect status %q", status)
		return model.DefectFinding{}
	}
}

func (f completionFixture) transitionDefect(t *testing.T, defect model.DefectFinding, status, reason string) model.DefectFinding {
	t.Helper()
	updated, err := f.defects.Transition(context.Background(), defect.ID,
		dto.TransitionRequest{Status: status, ExpectedVersion: defect.Version, Reason: reason},
		"operator", "req-defect-"+status)
	if err != nil {
		t.Fatalf("transition defect %s -> %s: %v", defect.Status, status, err)
	}
	return updated
}

func (f completionFixture) seedFinalizedPriority(t *testing.T, code, defectCode, status string) {
	t.Helper()
	priority := model.PriorityDecision{
		BaseModel: model.BaseModel{Code: code, Name: "处置优先级 " + code, Status: status, Version: 2},
		Facility: "K42 桥梁", Owner: "处置组", Category: "结构", RiskLevel: "critical",
		EffectiveAt: time.Now().UTC(), Evidence: "等级评定表与会审结论",
		RelatedCode: defectCode, PreparedBy: "operator",
	}
	if err := f.db.Create(&priority).Error; err != nil {
		t.Fatalf("seed finalized priority: %v", err)
	}
}

func completeRound(t *testing.T, f completionFixture, round model.InspectionRound) (model.InspectionRound, error) {
	t.Helper()
	return f.rounds.Transition(context.Background(), round.ID,
		dto.TransitionRequest{Status: "completed", ExpectedVersion: round.Version, Reason: "缺陷处置核验通过，批次完成"},
		"reviewer", "req-round-complete")
}

func auditCount(t *testing.T, f completionFixture, entityType, action string) int64 {
	t.Helper()
	logs, _, err := f.auditRepo.ListAudits(context.Background(), 1, 200, "")
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	var count int64
	for _, log := range logs {
		if log.EntityType == entityType && log.Action == action {
			count++
		}
	}
	return count
}

func TestCompletionBlockedByNewOrVerifiedDefect(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-BLOCK-1")
	f.createDefect(t, "DF-BLOCK-NEW", round.Code, "new", "low", false)
	f.createDefect(t, "DF-BLOCK-VER", round.Code, "verified", "medium", false)

	_, err := completeRound(t, f, round)
	if !errors.Is(err, ErrCompletionBlocked) {
		t.Fatalf("expected completion blocked, got %v", err)
	}

	// Batch, defects and audit stay untouched.
	stored, err := f.roundRepo.Get(context.Background(), round.ID)
	if err != nil {
		t.Fatalf("reload round: %v", err)
	}
	if stored.Status != "review" || stored.Version != 1 {
		t.Fatalf("round mutated on blocked completion: status=%s version=%d", stored.Status, stored.Version)
	}
	if auditCount(t, f, "InspectionRound", "transition") != 0 {
		t.Fatal("blocked completion must not append a round transition audit")
	}

	// Blocked verification is persisted with blocker codes and reasons.
	view, exists, err := f.rounds.LatestCompletionCheck(context.Background(), round.Code)
	if err != nil || !exists {
		t.Fatalf("expected blocked verification record, exists=%v err=%v", exists, err)
	}
	if view.Status != model.CompletionCheckBlocked || len(view.BlockerCodes) != 2 {
		t.Fatalf("unexpected blocked view: %+v", view)
	}
	reasons := map[string]string{}
	for _, defect := range view.Details.Defects {
		reasons[defect.DefectCode] = defect.BlockerReason
	}
	if reasons["DF-BLOCK-NEW"] != model.CompletionBlockPendingDisposition ||
		reasons["DF-BLOCK-VER"] != model.CompletionBlockPendingDisposition {
		t.Fatalf("expected pending disposition blockers, got %+v", reasons)
	}
}

func TestCompletionBlockedByMissingBasis(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-BLOCK-2")
	// monitoring without any transition audit or finalized priority basis.
	f.createDefect(t, "DF-BLOCK-BASIS", round.Code, "monitoring", "low", false)

	if _, err := completeRound(t, f, round); !errors.Is(err, ErrCompletionBlocked) {
		t.Fatalf("expected missing basis block, got %v", err)
	}
	view, _, _ := f.rounds.LatestCompletionCheck(context.Background(), round.Code)
	for _, defect := range view.Details.Defects {
		if defect.DefectCode == "DF-BLOCK-BASIS" && defect.BlockerReason != model.CompletionBlockMissingBasis {
			t.Fatalf("expected missing_basis, got %q", defect.BlockerReason)
		}
	}
}

func TestCompletionBlockedForSevereDefectWithoutFinalizedPriority(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-BLOCK-3")
	f.createDefect(t, "DF-SEVERE", round.Code, "monitoring", "critical", true)
	f.createDefect(t, "DF-HIGH", round.Code, "mitigated", "high", true)

	if _, err := completeRound(t, f, round); !errors.Is(err, ErrCompletionBlocked) {
		t.Fatalf("expected severe risk block, got %v", err)
	}
	view, _, _ := f.rounds.LatestCompletionCheck(context.Background(), round.Code)
	blocked := map[string]string{}
	for _, defect := range view.Details.Defects {
		blocked[defect.DefectCode] = defect.BlockerReason
	}
	if blocked["DF-SEVERE"] != model.CompletionBlockMissingPriority || blocked["DF-HIGH"] != model.CompletionBlockMissingPriority {
		t.Fatalf("severe defects must require finalized priority, got %+v", blocked)
	}

	// A draft priority does not satisfy the gate.
	f.seedFinalizedPriority(t, "PD-DRAFT", "DF-SEVERE", "draft")
	if _, err := completeRound(t, f, round); !errors.Is(err, ErrCompletionBlocked) {
		t.Fatalf("draft priority must not satisfy the gate, got %v", err)
	}
}

func TestCompletionSucceedsAfterDispositionAndPriority(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-PASS-1")
	low := f.createDefect(t, "DF-PASS-LOW", round.Code, "monitoring", "low", true)
	severe := f.createDefect(t, "DF-PASS-CRIT", round.Code, "mitigated", "critical", true)
	f.seedFinalizedPriority(t, "PD-PASS-1", severe.Code, "urgent")

	completed, err := completeRound(t, f, round)
	if err != nil {
		t.Fatalf("completion should pass: %v", err)
	}
	if completed.Status != "completed" || completed.Version != 2 {
		t.Fatalf("unexpected completed round: %+v", completed)
	}
	if completed.LatestCompletionCheck == nil || completed.LatestCompletionCheck.Status != model.CompletionCheckPassed {
		t.Fatalf("completed round must carry passing verification, got %+v", completed.LatestCompletionCheck)
	}
	if completed.LatestCompletionCheck.RelatedDefectCount != 2 {
		t.Fatalf("expected two verified defects, got %d", completed.LatestCompletionCheck.RelatedDefectCount)
	}

	// Defects and the finalized priority are untouched.
	reloadLow, err := f.defects.Get(context.Background(), low.ID)
	if err != nil || reloadLow.Status != "monitoring" {
		t.Fatalf("low defect mutated: %+v err=%v", reloadLow, err)
	}
	reloadSevere, err := f.defects.Get(context.Background(), severe.ID)
	if err != nil || reloadSevere.Status != "mitigated" {
		t.Fatalf("severe defect mutated: %+v err=%v", reloadSevere, err)
	}
	if reloadSevere.CompletionCheck == nil || reloadSevere.CompletionCheck.RoundCode != round.Code {
		t.Fatalf("defect page must re-read passing verification: %+v", reloadSevere.CompletionCheck)
	}
	if auditCount(t, f, "InspectionRound", "transition") != 1 {
		t.Fatal("exactly one round transition audit expected")
	}
}

func TestCompletionWithoutRelatedDefectsPasses(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-PASS-EMPTY")
	completed, err := completeRound(t, f, round)
	if err != nil {
		t.Fatalf("round without defects should complete: %v", err)
	}
	if completed.LatestCompletionCheck == nil || completed.LatestCompletionCheck.Status != model.CompletionCheckPassed {
		t.Fatalf("expected passing verification even with zero defects: %+v", completed.LatestCompletionCheck)
	}
}

func TestAssociationViaRoundRelatedCode(t *testing.T) {
	f := newCompletionFixture(t)
	// Seed-style chain: round.relatedCode == defect.relatedCode (REL-XXX).
	round := f.createReviewRound(t, "IR-CHAIN")
	round.RelatedCode = "REL-CHAIN-01"
	if err := f.db.Model(&model.InspectionRound{}).Where("id = ?", round.ID).Update("related_code", "REL-CHAIN-01").Error; err != nil {
		t.Fatalf("set round relatedCode: %v", err)
	}
	f.createDefect(t, "DF-CHAIN", "REL-CHAIN-01", "new", "low", false)

	if _, err := completeRound(t, f, round); !errors.Is(err, ErrCompletionBlocked) {
		t.Fatalf("defect linked via round.relatedCode must block completion, got %v", err)
	}
	view, exists, err := f.rounds.LatestCompletionCheck(context.Background(), round.Code)
	if err != nil || !exists || len(view.BlockerCodes) != 1 || view.BlockerCodes[0] != "DF-CHAIN" {
		t.Fatalf("expected DF-CHAIN blocker, got %+v exists=%v err=%v", view.BlockerCodes, exists, err)
	}
}

func TestBlockedThenFixedThenCompletedKeepsHistory(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-RETRY")
	severe := f.createDefect(t, "DF-RETRY", round.Code, "new", "high", false)

	if _, err := completeRound(t, f, round); !errors.Is(err, ErrCompletionBlocked) {
		t.Fatalf("first attempt must block, got %v", err)
	}
	severe = f.transitionDefect(t, severe, "verified", "现场复核确认缺陷存在")
	severe = f.transitionDefect(t, severe, "monitoring", "纳入在线监测，每周复核变形速率")
	if _, err := completeRound(t, f, round); !errors.Is(err, ErrCompletionBlocked) {
		t.Fatalf("second attempt must still block on missing priority, got %v", err)
	}
	f.seedFinalizedPriority(t, "PD-RETRY", severe.Code, "restrict")
	if _, err := completeRound(t, f, round); err != nil {
		t.Fatalf("third attempt must pass, got %v", err)
	}

	// Latest check is passing; the earlier blocked rows remain readable.
	view, exists, err := f.rounds.LatestCompletionCheck(context.Background(), round.Code)
	if err != nil || !exists {
		t.Fatalf("latest check missing: exists=%v err=%v", exists, err)
	}
	if view.Status != model.CompletionCheckPassed {
		t.Fatalf("expected latest passing check, got %s", view.Status)
	}
	ids, err := f.roundRepo.ListIDs(context.Background())
	if err != nil {
		t.Fatalf("list round ids: %v", err)
	}
	latestMap, err := f.checkRepo.LatestByRoundIDs(context.Background(), ids)
	if err != nil {
		t.Fatalf("latest by ids: %v", err)
	}
	if latestMap[round.ID].Status != model.CompletionCheckPassed {
		t.Fatalf("enrichment map wrong status: %+v", latestMap[round.ID])
	}
}

func TestDuplicateCompletionSucceedsOnce(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-DUP")
	f.createDefect(t, "DF-DUP", round.Code, "closed", "low", true)

	if _, err := completeRound(t, f, round); err != nil {
		t.Fatalf("first completion: %v", err)
	}
	if _, err := completeRound(t, f, round); err == nil {
		t.Fatal("duplicate completion must fail")
	} else if !errors.Is(err, repository.ErrVersionConflict) && !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("duplicate completion must conflict or reject the transition, got %v", err)
	}
	stored, _ := f.roundRepo.Get(context.Background(), round.ID)
	if stored.Status != "completed" || stored.Version != 2 {
		t.Fatalf("round mutated by duplicate completion: %+v", stored)
	}
	if auditCount(t, f, "InspectionRound", "transition") != 1 {
		t.Fatal("duplicate completion appended a second transition audit")
	}
}

func TestConcurrentCompletionSucceedsOnce(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-CONC")
	f.createDefect(t, "DF-CONC", round.Code, "monitoring", "low", true)

	const workers = 8
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.rounds.Transition(context.Background(), round.ID,
				dto.TransitionRequest{Status: "completed", ExpectedVersion: 1, Reason: "并发完成核验"},
				"reviewer", fmt.Sprintf("req-conc-%d", time.Now().UnixNano()))
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	successes, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, repository.ErrVersionConflict), errors.Is(err, ErrInvalidTransition):
			rejected++
		default:
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || rejected != workers-1 {
		t.Fatalf("expected exactly one success, got successes=%d rejected=%d", successes, rejected)
	}
	stored, _ := f.roundRepo.Get(context.Background(), round.ID)
	if stored.Status != "completed" || stored.Version != 2 {
		t.Fatalf("concurrent completion corrupted round: %+v", stored)
	}
	if auditCount(t, f, "InspectionRound", "transition") != 1 {
		t.Fatal("concurrent completion appended multiple transition audits")
	}
}

func TestOnlyCompletionTargetTriggersVerification(t *testing.T) {
	f := newCompletionFixture(t)
	round := f.createReviewRound(t, "IR-OTHER")
	f.createDefect(t, "DF-OTHER", round.Code, "new", "low", false)

	// review -> running is not gated by defect disposition verification.
	running, err := f.rounds.Transition(context.Background(), round.ID,
		dto.TransitionRequest{Status: "running", ExpectedVersion: 1, Reason: "退回现场补充检查"},
		"operator", "req-back-running")
	if err != nil {
		t.Fatalf("review -> running should bypass gate: %v", err)
	}
	if running.Status != "running" || running.LatestCompletionCheck != nil {
		t.Fatalf("non-completion transition must not attach checks: %+v", running)
	}
}
