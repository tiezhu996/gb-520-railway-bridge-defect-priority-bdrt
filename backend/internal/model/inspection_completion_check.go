package model

import (
	"encoding/json"
	"strings"
	"time"
)

// InspectionCompletionCheck persists the latest 缺陷处置核验 result for an
// 检查批次. Exactly one row exists per inspection round: a blocked attempt is
// written without touching the round, defects, decisions or audit trail, so
// the verification outcome remains readable after refresh. A later attempt
// (blocked again or finally passed) overwrites the same row.
type InspectionCompletionCheck struct {
	ID                uint      `json:"id" gorm:"primaryKey"`
	InspectionRoundID uint      `json:"inspectionRoundId" gorm:"uniqueIndex;not null"`
	RoundCode         string    `json:"roundCode" gorm:"size:64;index;not null"`
	Passed            bool      `json:"passed" gorm:"index;not null"`
	DefectTotal       int       `json:"defectTotal" gorm:"not null;default:0"`
	CheckedDefectIDs  string    `json:"checkedDefectIds" gorm:"size:1000;not null;default:''"`
	Blockers          string    `json:"blockers" gorm:"type:text;not null;default:''"`
	Actor             string    `json:"actor" gorm:"size:80;index;not null"`
	RequestID         string    `json:"requestId" gorm:"size:64;index;not null"`
	CheckedAt         time.Time `json:"checkedAt" gorm:"index;not null"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func (InspectionCompletionCheck) TableName() string { return "inspection_completion_checks" }

// MarshalJSON exposes the stored blocker payload as structured JSON so API
// consumers can list 阻塞编号 without parsing the text column themselves.
func (check InspectionCompletionCheck) MarshalJSON() ([]byte, error) {
	type alias InspectionCompletionCheck
	blockers := make([]CompletionBlocker, 0)
	if strings.TrimSpace(check.Blockers) != "" {
		_ = json.Unmarshal([]byte(check.Blockers), &blockers)
	}
	return json.Marshal(struct {
		alias
		Blockers []CompletionBlocker `json:"blockers"`
	}{alias: alias(check), Blockers: blockers})
}

// CompletionBlocker describes one blocking defect with its code and reason.
// It is the JSON payload element stored in InspectionCompletionCheck.Blockers
// and returned to the API/UI so the page can list 阻塞编号.
type CompletionBlocker struct {
	DefectID   uint   `json:"defectId"`
	DefectCode string `json:"defectCode"`
	Status     string `json:"status"`
	RiskLevel  string `json:"riskLevel"`
	Reason     string `json:"reason"`
	Code       string `json:"code"`
}

const (
	// BlockerUndisposed: the defect is still new/verified when the round
	// leaves review, so no disposition has happened yet.
	BlockerUndisposed = "defect_undisposed"
	// BlockerMissingBasis: the defect reached monitoring/mitigated/closed
	// without a written disposition basis.
	BlockerMissingBasis = "defect_missing_basis"
	// BlockerMissingPriority: a high/critical defect has no finalized
	// (non-draft) priority decision.
	BlockerMissingPriority = "defect_missing_priority"
)
