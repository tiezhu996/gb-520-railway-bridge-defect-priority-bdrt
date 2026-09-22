package repository

import (
	"context"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/dto"
	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
	"gorm.io/gorm"
)

// InspectionRoundRepository owns all persistence operations for 检查批次.
type InspectionRoundRepository interface {
	List(context.Context, dto.PageQuery) (Page[model.InspectionRound], error)
	Get(context.Context, uint) (model.InspectionRound, error)
	Create(context.Context, *model.InspectionRound) error
	Update(context.Context, uint, uint, *model.InspectionRound) error
	Delete(context.Context, uint) error
	CountByStatus(context.Context) (map[string]int64, error)
	// InTransaction runs fn inside one database transaction so the completion
	// gate can read related defects/decisions and write the round plus its
	// verification result atomically.
	InTransaction(context.Context, func(tx *gorm.DB) error) error
	// GetTx reads the round using the given transaction handle.
	GetTx(ctx context.Context, tx *gorm.DB, id uint) (model.InspectionRound, error)
	// CompleteConditional moves the round to target only while its optimistic
	// version still matches, and upserts the completion check in the same tx.
	CompleteConditional(ctx context.Context, tx *gorm.DB, id, expectedVersion uint, round *model.InspectionRound, check *model.InspectionCompletionCheck) error
}

type inspectionRoundRepository struct {
	db    *gorm.DB
	store *Store[model.InspectionRound]
}

func NewInspectionRoundRepository(db *gorm.DB) InspectionRoundRepository {
	return &inspectionRoundRepository{db: db, store: NewStore[model.InspectionRound](db)}
}

func (r *inspectionRoundRepository) List(ctx context.Context, q dto.PageQuery) (Page[model.InspectionRound], error) {
	return r.store.List(ctx, q)
}
func (r *inspectionRoundRepository) Get(ctx context.Context, id uint) (model.InspectionRound, error) {
	return r.store.Get(ctx, id)
}
func (r *inspectionRoundRepository) Create(ctx context.Context, item *model.InspectionRound) error {
	return r.store.Create(ctx, item)
}
func (r *inspectionRoundRepository) Update(ctx context.Context, id, version uint, item *model.InspectionRound) error {
	return r.store.Update(ctx, id, version, item)
}
func (r *inspectionRoundRepository) Delete(ctx context.Context, id uint) error {
	return r.store.Delete(ctx, id)
}
func (r *inspectionRoundRepository) CountByStatus(ctx context.Context) (map[string]int64, error) {
	return r.store.CountByStatus(ctx)
}

func (r *inspectionRoundRepository) InTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(fn)
}

func (r *inspectionRoundRepository) GetTx(ctx context.Context, tx *gorm.DB, id uint) (model.InspectionRound, error) {
	var round model.InspectionRound
	if err := tx.WithContext(ctx).First(&round, id).Error; err != nil {
		return model.InspectionRound{}, err
	}
	return round, nil
}

func (r *inspectionRoundRepository) CompleteConditional(ctx context.Context, tx *gorm.DB, id, expectedVersion uint, round *model.InspectionRound, check *model.InspectionCompletionCheck) error {
	result := tx.WithContext(ctx).Model(&model.InspectionRound{}).
		Where("id = ? AND version = ?", id, expectedVersion).
		Select("*").Omit("id", "code", "created_at", "deleted_at").Updates(round)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrVersionConflict
	}
	return upsertCompletionCheck(ctx, tx, check)
}

// upsertCompletionCheck keeps exactly one verification row per round. A
// re-attempt (blocked or passed) overwrites the previous outcome.
func upsertCompletionCheck(ctx context.Context, tx *gorm.DB, check *model.InspectionCompletionCheck) error {
	var existing model.InspectionCompletionCheck
	err := tx.WithContext(ctx).
		Where("inspection_round_id = ?", check.InspectionRoundID).First(&existing).Error
	if err == nil {
		check.ID = existing.ID
		check.CreatedAt = existing.CreatedAt
		return tx.WithContext(ctx).Model(&model.InspectionCompletionCheck{}).
			Where("id = ?", existing.ID).
			Select("*").Omit("id", "created_at").Updates(check).Error
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	return tx.WithContext(ctx).Create(check).Error
}
