package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
	"gorm.io/gorm"
)

// CompletionCheckRepository persists 缺陷处置核验 outcomes and performs the
// transactional cross-aggregate reads required while completing a batch.
type CompletionCheckRepository interface {
	// Upsert saves the latest outcome for a round. A blocked attempt is saved
	// in its own transaction so business aggregates stay untouched.
	Upsert(context.Context, *model.InspectionCompletionCheck) error
	// List returns the latest check for every round, newest first.
	List(context.Context) ([]model.InspectionCompletionCheck, error)
	// GetByRound returns the check stored for one round, if any.
	GetByRound(context.Context, uint) (model.InspectionCompletionCheck, error)
	// DefectsForRoundTx lists defects whose relatedCode points at the round
	// code (case-insensitive), using the supplied transaction handle.
	DefectsForRoundTx(ctx context.Context, tx *gorm.DB, roundCode string) ([]model.DefectFinding, error)
	// FinalizedPrioritiesForCodesTx returns finalized (non-draft) decisions
	// whose relatedCode matches one of the supplied defect codes.
	FinalizedPrioritiesForCodesTx(ctx context.Context, tx *gorm.DB, codes []string) ([]model.PriorityDecision, error)
}

type completionCheckRepository struct{ db *gorm.DB }

func NewCompletionCheckRepository(db *gorm.DB) CompletionCheckRepository {
	return &completionCheckRepository{db: db}
}

func (r *completionCheckRepository) Upsert(ctx context.Context, check *model.InspectionCompletionCheck) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return upsertCompletionCheck(ctx, tx, check)
	})
}

func (r *completionCheckRepository) List(ctx context.Context) ([]model.InspectionCompletionCheck, error) {
	items := make([]model.InspectionCompletionCheck, 0)
	err := r.db.WithContext(ctx).Order("checked_at DESC, id DESC").Find(&items).Error
	return items, err
}

func (r *completionCheckRepository) GetByRound(ctx context.Context, roundID uint) (model.InspectionCompletionCheck, error) {
	var check model.InspectionCompletionCheck
	err := r.db.WithContext(ctx).Where("inspection_round_id = ?", roundID).First(&check).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.InspectionCompletionCheck{}, nil
	}
	return check, err
}

func (r *completionCheckRepository) DefectsForRoundTx(ctx context.Context, tx *gorm.DB, roundCode string) ([]model.DefectFinding, error) {
	defects := make([]model.DefectFinding, 0)
	err := tx.WithContext(ctx).
		Where("UPPER(TRIM(related_code)) = ?", strings.ToUpper(strings.TrimSpace(roundCode))).
		Order("id ASC").Find(&defects).Error
	return defects, err
}

func (r *completionCheckRepository) FinalizedPrioritiesForCodesTx(ctx context.Context, tx *gorm.DB, codes []string) ([]model.PriorityDecision, error) {
	decisions := make([]model.PriorityDecision, 0)
	if len(codes) == 0 {
		return decisions, nil
	}
	upperCodes := make([]string, 0, len(codes))
	for _, code := range codes {
		upperCodes = append(upperCodes, strings.ToUpper(strings.TrimSpace(code)))
	}
	err := tx.WithContext(ctx).
		Where("status <> ?", model.PriorityDecisionInitialStatus).
		Where("UPPER(TRIM(related_code)) IN ?", upperCodes).
		Find(&decisions).Error
	return decisions, err
}
