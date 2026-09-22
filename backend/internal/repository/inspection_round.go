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
	ListIDs(ctx context.Context) ([]uint, error)
	ListCodeRefs(ctx context.Context) ([]model.InspectionRoundCodeRef, error)
	CompleteWithCheck(ctx context.Context, id, version uint, round *model.InspectionRound, check *model.InspectionCompletionCheck) error
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

// ListIDs returns every non-deleted round id in stable order.
func (r *inspectionRoundRepository) ListIDs(ctx context.Context) ([]uint, error) {
	ids := make([]uint, 0)
	err := r.db.WithContext(ctx).Model(&model.InspectionRound{}).
		Order("id ASC").Pluck("id", &ids).Error
	return ids, err
}

// ListCodeRefs returns id/code/relatedCode for every non-deleted round so the
// completion gate can resolve defect associations via either reference.
func (r *inspectionRoundRepository) ListCodeRefs(ctx context.Context) ([]model.InspectionRoundCodeRef, error) {
	refs := make([]model.InspectionRoundCodeRef, 0)
	err := r.db.WithContext(ctx).Model(&model.InspectionRound{}).
		Select("id, code, related_code").Order("id ASC").Scan(&refs).Error
	return refs, err
}

// CompleteWithCheck performs the optimistic-locked review->completed update and
// inserts the passing verification record in one transaction. A version
// conflict affects no rows, so duplicate or concurrent completions can never
// create a second completed transition or a passing check.
func (r *inspectionRoundRepository) CompleteWithCheck(ctx context.Context, id, version uint, round *model.InspectionRound, check *model.InspectionCompletionCheck) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.InspectionRound{}).
			Where("id = ? AND version = ?", id, version).
			Select("*").Omit("id", "code", "created_at", "deleted_at").
			Updates(round)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrVersionConflict
		}
		return tx.Create(check).Error
	})
}
