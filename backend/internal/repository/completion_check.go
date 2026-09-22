package repository

import (
	"context"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
	"gorm.io/gorm"
)

// CompletionCheckRepository owns persistence of 检查批次完成前缺陷处置核验 records.
type CompletionCheckRepository interface {
	Create(ctx context.Context, check *model.InspectionCompletionCheck) error
	LatestByRoundIDs(ctx context.Context, roundIDs []uint) (map[uint]model.InspectionCompletionCheck, error)
	LatestByRoundCode(ctx context.Context, roundCode string) (model.InspectionCompletionCheck, error)
	LatestByRoundCodes(ctx context.Context, roundCodes []string) (map[string]model.InspectionCompletionCheck, error)
	LatestPassedByDefectCodes(ctx context.Context, defectCodes []string) (map[string]model.InspectionCompletionCheck, error)
}

type completionCheckRepository struct{ db *gorm.DB }

func NewCompletionCheckRepository(db *gorm.DB) CompletionCheckRepository {
	return &completionCheckRepository{db: db}
}

func (r *completionCheckRepository) Create(ctx context.Context, check *model.InspectionCompletionCheck) error {
	return r.db.WithContext(ctx).Create(check).Error
}

// LatestByRoundIDs returns the most recent verification for each given round.
// The MAX(id) grouping works on both PostgreSQL and SQLite; callers receive an
// empty map for rounds without any verification.
func (r *completionCheckRepository) LatestByRoundIDs(ctx context.Context, roundIDs []uint) (map[uint]model.InspectionCompletionCheck, error) {
	result := make(map[uint]model.InspectionCompletionCheck)
	if len(roundIDs) == 0 {
		return result, nil
	}
	latest := r.db.WithContext(ctx).Model(&model.InspectionCompletionCheck{}).
		Select("MAX(id) AS id").Where("inspection_round_id IN ?", roundIDs).
		Group("inspection_round_id")
	var rows []model.InspectionCompletionCheck
	err := r.db.WithContext(ctx).
		Where("id IN (?)", latest).
		Order("inspection_round_id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for index := range rows {
		result[rows[index].InspectionRoundID] = rows[index]
	}
	return result, nil
}

func (r *completionCheckRepository) LatestByRoundCode(ctx context.Context, roundCode string) (model.InspectionCompletionCheck, error) {
	var check model.InspectionCompletionCheck
	err := r.db.WithContext(ctx).
		Where("round_code = ?", roundCode).
		Order("id DESC").First(&check).Error
	return check, err
}

// LatestByRoundCodes returns the most recent verification (passed or blocked)
// keyed by round code. It is used to surface blocker information on the defect
// workbench, since defects associate to rounds through defect.relatedCode.
func (r *completionCheckRepository) LatestByRoundCodes(ctx context.Context, roundCodes []string) (map[string]model.InspectionCompletionCheck, error) {
	result := make(map[string]model.InspectionCompletionCheck)
	if len(roundCodes) == 0 {
		return result, nil
	}
	var rows []model.InspectionCompletionCheck
	latest := r.db.WithContext(ctx).Model(&model.InspectionCompletionCheck{}).
		Select("MAX(id) AS id").Where("round_code IN ?", roundCodes).
		Group("round_code")
	if err := r.db.WithContext(ctx).
		Where("id IN (?)", latest).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for index := range rows {
		result[rows[index].RoundCode] = rows[index]
	}
	return result, nil
}

// LatestPassedByDefectCodes returns, keyed by defect code, the most recent
// passing verification that covered each defect. Rows are ordered newest-first
// and the Details JSON is decoded to confirm membership, so the LIKE filter is
// only a candidate pre-filter and stays portable across PostgreSQL and SQLite.
func (r *completionCheckRepository) LatestPassedByDefectCodes(ctx context.Context, defectCodes []string) (map[string]model.InspectionCompletionCheck, error) {
	result := make(map[string]model.InspectionCompletionCheck)
	if len(defectCodes) == 0 {
		return result, nil
	}
	query := r.db.WithContext(ctx).
		Where("status = ?", model.CompletionCheckPassed)
	for index, code := range defectCodes {
		pattern := "%" + defectJSONMarker(code) + "%"
		if index == 0 {
			query = query.Where("details LIKE ?", pattern)
		} else {
			query = query.Or("status = ? AND details LIKE ?", model.CompletionCheckPassed, pattern)
		}
	}
	var rows []model.InspectionCompletionCheck
	if err := query.Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	codeSet := make(map[string]bool, len(defectCodes))
	for _, code := range defectCodes {
		codeSet[code] = true
	}
	for index := range rows {
		details, err := parseCheckDetails(rows[index].Details)
		if err != nil {
			return nil, err
		}
		for _, defect := range details.Defects {
			if codeSet[defect.DefectCode] {
				if _, exists := result[defect.DefectCode]; !exists {
					result[defect.DefectCode] = rows[index]
				}
			}
		}
	}
	return result, nil
}
