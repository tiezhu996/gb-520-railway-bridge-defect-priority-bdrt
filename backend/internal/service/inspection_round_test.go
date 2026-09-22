package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	rounds   repository.InspectionRoundRepository
	defects  repository.DefectFindingRepository
	checks   repository.CompletionCheckRepository
	roundsS  InspectionRoundService
	defectsS DefectFindingService
	priority PriorityDecisionService
	db       *gorm.DB
}

func newCompletionFixture(t *testing.T) completionFixture {
	t.Helper()
	// File-backed SQLite serialized through one connection avoids SQLite's
	// lock-upgrade SQLITE_BUSY, so the optimistic predicate is what actually
	// rejects duplicate completions (PostgreSQL uses row locks instead).
	dsn := fmt.Sprintf("file:%s", strings.ReplaceAll(t.TempDir()+"/completion.db", " ", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap sqlite: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&model.User{}, &model.AuditLog{},
		&model.InspectionRound{}, &model.DefectFinding{},
		&model.PriorityDecision{}, &model.PriorityDecisionRevision{},
		&model.InspectionCompletionCheck{},
	); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	security := NewSecurityService(repository.NewSecurityRepository(db), config.Config{})
	rounds := repository.NewInspectionRoundRepository(db)
	defects := repository.NewDefectFindingRepository(db)
	priorities := repository.NewPriorityDecisionRepository(db)
	completionChecks := repository.NewCompletionCheckRepository(db)
	return completionFixture{
		rounds:   rounds,
		defects:  defects,
		checks:   completionChecks,
		roundsS:  NewInspectionRoundService(rounds, completionChecks, security),
		defectsS: NewDefectFindingService(defects, security),
		priority: NewPriorityDecisionService(priorities, security),
		db:       db,
	}
}

func (f completionFixture) createRound(t *testing.T, code, status string) model.InspectionRound {
	t.Helper()
	round := model.InspectionRound{
		BaseModel: model.BaseModel{Code: code, Name: "检查批次" + code, Status: status, Version: 1},
		Facility: "K42", Owner: "operator", Category: "结构", RiskLevel: "high",
		EffectiveAt: time.Now().UTC(), Evidence: "round evidence",
	}
	if err := f.rounds.Create(context.Background(), &round); err != nil {
		t.Fatalf("create round: %v", err)
	}
	return round
}

func (f completionFixture) createDefect(t *testing.T, code, roundCode, status, risk string) model.DefectFinding {
	t.Helper()
	defect := model.DefectFinding{
		BaseModel: model.BaseModel{Code: code, Name: "缺陷" + code, Status: status, Version: 1},
		Facility: "K42", Owner: "operator", Category: "结构", RiskLevel: risk,
		EffectiveAt: time.Now().UTC(), Evidence: "defect evidence", RelatedCode: roundCode,
	}
	if err := f.defects.Create(context.Background(), &defect); err != nil {
		t.Fatalf("create defect: %v", err)
	}
	return defect
}

func (f completionFixture) transitionDefect(t *testing.T, defect model.DefectFinding, target, reason string) model.DefectFinding {
	t.Helper()
	updated, err := f.defectsS.Transition(context.Background(), defect.ID,
		dto.TransitionRequest{Status: target, ExpectedVersion: defect.Version, Reason: reason},
		"operator", "req-defect-"+target)
	if err != nil {
		t.Fatalf("transition defect %s -> %s: %v", defect.Code, target, err)
	}
	return updated
}

func completeInput(version uint) dto.TransitionRequest {
	return dto.TransitionRequest{Status: "completed", ExpectedVersion: version, Reason: "复核完成并核验缺陷处置"}
}

func TestCompletionBlockedByUndisposedDefect(t *testing.T) {
	f := newCompletionFixture(t)
	ctx := context.Background()
	round := f.createRound(t, "IR-B1", "review")
	f.createDefect(t, "DF-B1", round.Code, "new", "low")

	_, err := f.roundsS.Transition(ctx, round.ID, completeInput(round.Version), "reviewer", "req-block-1")
	var blocked *ErrCompletionBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("expected completion blocked error, got %v", err)
	}
	if len(blocked.Blockers) != 1 || blocked.Blockers[0].Code != model.BlockerUndisposed || blocked.Blockers[0].DefectCode != "DF-B1" {
		t.Fatalf("unexpected blockers: %+v", blocked.Blockers)
	}

	// Failure must not modify the round, defects or append audit.
	stored, err := f.rounds.Get(ctx, round.ID)
	if err != nil || stored.Status != "review" || stored.Version != 1 {
		t.Fatalf("round changed after blocked completion: %+v err=%v", stored, err)
	}
	var auditCount int64
	if err := f.db.Model(&model.AuditLog{}).Where("entity_type = ?", "InspectionRound").Count(&auditCount).Error; err != nil || auditCount != 0 {
		t.Fatalf("expected no round audit on block, got count=%d err=%v", auditCount, err)
	}
	// The blocked result is persisted and readable after refresh.
	check, err := f.roundsS.CompletionCheckForRound(ctx, round.ID)
	if err != nil || check.Passed || check.DefectTotal != 1 || !strings.Contains(check.Blockers, "DF-B1") {
		t.Fatalf("expected blocked check persisted, got %+v err=%v", check, err)
	}
}

func TestCompletionBlockedByMissingBasisAndPriority(t *testing.T) {
	f := newCompletionFixture(t)
	ctx := context.Background()
	round := f.createRound(t, "IR-B2", "review")
	// monitoring defect without basis
	noBasis := f.createDefect(t, "DF-B2A", round.Code, "monitoring", "medium")
	if err := f.db.Model(&model.DefectFinding{}).Where("id = ?", noBasis.ID).
		Update("disposition_basis", "").Error; err != nil {
		t.Fatalf("clear basis: %v", err)
	}
	// critical mitigated defect with basis but no finalized priority
	withBasis := f.createDefect(t, "DF-B2B", round.Code, "mitigated", "critical")
	if err := f.db.Model(&model.DefectFinding{}).Where("id = ?", withBasis.ID).
		Update("disposition_basis", "立即限速并加固，复测合格").Error; err != nil {
		t.Fatalf("set basis: %v", err)
	}

	_, err := f.roundsS.Transition(ctx, round.ID, completeInput(1), "reviewer", "req-block-2")
	var blocked *ErrCompletionBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("expected blocked, got %v", err)
	}
	codes := map[string]string{}
	for _, blocker := range blocked.Blockers {
		codes[blocker.DefectCode] = blocker.Code
	}
	if codes["DF-B2A"] != model.BlockerMissingBasis {
		t.Fatalf("DF-B2A should miss basis, got %v", codes)
	}
	if codes["DF-B2B"] != model.BlockerMissingPriority {
		t.Fatalf("DF-B2B should miss finalized priority, got %v", codes)
	}
}

func TestCompletionSucceedsAfterDispositionAndFinalizedPriority(t *testing.T) {
	f := newCompletionFixture(t)
	ctx := context.Background()
	round := f.createRound(t, "IR-G1", "review")

	lowDefect := f.createDefect(t, "DF-G1", round.Code, "new", "low")
	lowDefect = f.transitionDefect(t, lowDefect, "monitoring", "纳入周期监测，季度复测")
	if strings.TrimSpace(lowDefect.DispositionBasis) == "" {
		t.Fatal("disposition basis should be written on monitoring transition")
	}

	critical := f.createDefect(t, "DF-G2", round.Code, "verified", "critical")
	critical = f.transitionDefect(t, critical, "mitigated", "腹板裂缝已加固并复测")

	// Create a draft priority and finalize it through independent review.
	created, err := f.priority.Create(ctx, dto.CreatePriorityDecision{
		Code: "PD-G1", Name: "优先级决定", Facility: "K42", Owner: "operator", Category: "结构",
		RiskLevel: "critical", EffectiveAt: time.Now().UTC(), Evidence: "裂缝照片与量测记录",
		RelatedCode: critical.Code,
	}, "operator", "req-pd-create")
	if err != nil {
		t.Fatalf("create priority: %v", err)
	}
	if _, err := f.priority.Transition(ctx, created.ID,
		dto.TransitionRequest{Status: "urgent", ExpectedVersion: created.Version, Reason: "独立复核确认立即处置"},
		"reviewer", model.RoleReviewer, "req-pd-final"); err != nil {
		t.Fatalf("finalize priority: %v", err)
	}

	completed, err := f.roundsS.Transition(ctx, round.ID, completeInput(1), "reviewer", "req-complete")
	if err != nil {
		t.Fatalf("completion should pass, got %v", err)
	}
	if completed.Status != "completed" || completed.Version != 2 {
		t.Fatalf("unexpected completed round: %+v", completed)
	}
	check, err := f.roundsS.CompletionCheckForRound(ctx, round.ID)
	if err != nil || !check.Passed || check.DefectTotal != 2 || check.Blockers != "[]" {
		t.Fatalf("expected passed check, got %+v err=%v", check, err)
	}
}

func TestCompletionConcurrentSucceedsOnce(t *testing.T) {
	f := newCompletionFixture(t)
	ctx := context.Background()
	round := f.createRound(t, "IR-C1", "review")
	defect := f.createDefect(t, "DF-C1", round.Code, "new", "low")
	defect = f.transitionDefect(t, defect, "monitoring", "纳入周期监测")
	f.transitionDefect(t, defect, "closed", "缺陷已消除并关闭")

	const workers = 8
	var wg sync.WaitGroup
	results := make([]error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, results[index] = f.roundsS.Transition(ctx, round.ID, completeInput(1), "reviewer",
				fmt.Sprintf("req-concurrent-%d", index))
		}(i)
	}
	close(start)
	wg.Wait()

	successes, rejected := 0, 0
	for _, result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, repository.ErrVersionConflict), errors.Is(result, ErrInvalidTransition):
			// Concurrent losers either hit the optimistic predicate inside
			// the gate or observe the already-completed round; both reject
			// without side effects.
			rejected++
		default:
			t.Fatalf("unexpected concurrent result: %v", result)
		}
	}
	if successes != 1 || rejected != workers-1 {
		t.Fatalf("expected exactly one success, got successes=%d rejected=%d", successes, rejected)
	}
	stored, err := f.rounds.Get(ctx, round.ID)
	if err != nil || stored.Status != "completed" || stored.Version != 2 {
		t.Fatalf("round final state wrong: %+v err=%v", stored, err)
	}
	var transitionAudits int64
	if err := f.db.Model(&model.AuditLog{}).
		Where("entity_type = ? AND action = ?", "InspectionRound", "transition").Count(&transitionAudits).Error; err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if transitionAudits != 1 {
		t.Fatalf("expected exactly one completion audit, got %d", transitionAudits)
	}
}

func TestCompletionDuplicateAfterSuccessRejected(t *testing.T) {
	f := newCompletionFixture(t)
	ctx := context.Background()
	round := f.createRound(t, "IR-D1", "review")
	// No linked defects -> verification passes immediately.
	completed, err := f.roundsS.Transition(ctx, round.ID, completeInput(1), "reviewer", "req-first")
	if err != nil {
		t.Fatalf("first completion: %v", err)
	}
	if _, err := f.roundsS.Transition(ctx, round.ID, completeInput(completed.Version), "reviewer", "req-duplicate"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("duplicate completion must be invalid transition, got %v", err)
	}
}

func TestCompletionGateOnlyAppliesFromReview(t *testing.T) {
	f := newCompletionFixture(t)
	ctx := context.Background()
	// running -> completed remains allowed without the gate.
	round := f.createRound(t, "IR-E1", "running")
	f.createDefect(t, "DF-E1", round.Code, "new", "critical")
	completed, err := f.roundsS.Transition(ctx, round.ID, completeInput(1), "operator", "req-running-complete")
	if err != nil || completed.Status != "completed" {
		t.Fatalf("running -> completed should stay ungated, got %+v err=%v", completed, err)
	}
}
