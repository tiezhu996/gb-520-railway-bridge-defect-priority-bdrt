package model

import (
	"encoding/json"
	"strings"
	"time"
)

// CompletionCheckStatus values for 检查批次完成前缺陷处置核验.
const (
	CompletionCheckPassed  = "passed"
	CompletionCheckBlocked = "blocked"
)

// InspectionCompletionCheck is an append-only record of the defect disposition
// verification performed before an 检查批次 leaves 复核 (review) for 完成
// (completed). Blocked attempts are persisted too, so operators can refresh
// the workbench and re-read the blocking defect codes. The record never mutates
// InspectionRound, DefectFinding or PriorityDecision rows: a blocked completion
// only inserts this verification row.
type InspectionCompletionCheck struct {
	ID                 uint      `json:"id" gorm:"primaryKey"`
	InspectionRoundID  uint      `json:"inspectionRoundId" gorm:"index:idx_completion_check_round,priority:1;not null"`
	RoundCode          string    `json:"roundCode" gorm:"size:64;index;not null"`
	Status             string    `json:"status" gorm:"size:24;index;not null"`
	Actor              string    `json:"actor" gorm:"size:80;index;not null"`
	RequestID          string    `json:"requestId" gorm:"size:64;index;not null"`
	RelatedDefectCount int       `json:"relatedDefectCount" gorm:"not null;default:0"`
	BlockerCodes       string    `json:"blockerCodes" gorm:"size:1000;not null;default:''"`
	Details            string    `json:"details" gorm:"type:text;not null;default:''"`
	CreatedAt          time.Time `json:"createdAt" gorm:"index:idx_completion_check_round,priority:2"`
}

func (InspectionCompletionCheck) TableName() string { return "inspection_completion_checks" }

// CompletionBlockReason values are mirrored by the frontend cell component.
const (
	CompletionBlockPendingDisposition = "pending_disposition"
	CompletionBlockMissingBasis       = "missing_basis"
	CompletionBlockMissingPriority    = "missing_finalized_priority"
)

// CompletionDefectDetail is the per-defect verification snapshot embedded in
// InspectionCompletionCheck.Details as JSON.
type CompletionDefectDetail struct {
	DefectID         uint   `json:"defectId"`
	DefectCode       string `json:"defectCode"`
	State            string `json:"state"`
	RiskLevel        string `json:"riskLevel"`
	Disposed         bool   `json:"disposed"`
	DispositionBasis string `json:"dispositionBasis"`
	PriorityCode     string `json:"priorityCode"`
	PriorityStatus   string `json:"priorityStatus"`
	BlockerReason    string `json:"blockerReason,omitempty"`
	BlockerMessage   string `json:"blockerMessage,omitempty"`
}

// CompletionCheckDetails is the JSON payload persisted with every verification.
type CompletionCheckDetails struct {
	RoundStatus string                   `json:"roundStatus"`
	Defects     []CompletionDefectDetail `json:"defects"`
}

// CompletionCheckView is the parsed, API-facing projection of a verification row.
type CompletionCheckView struct {
	InspectionRoundID  uint                   `json:"inspectionRoundId"`
	RoundCode          string                 `json:"roundCode"`
	Status             string                 `json:"status"`
	Actor              string                 `json:"actor"`
	RequestID          string                 `json:"requestId"`
	RelatedDefectCount int                    `json:"relatedDefectCount"`
	BlockerCodes       []string               `json:"blockerCodes"`
	Details            CompletionCheckDetails `json:"details"`
	CreatedAt          time.Time              `json:"createdAt"`
}

// DefectCompletionCheckView projects the latest passed batch verification that
// covered a single defect, so the defect workbench can show the disposition
// basis after refresh.
type DefectCompletionCheckView struct {
	InspectionRoundID uint                   `json:"inspectionRoundId"`
	RoundCode         string                 `json:"roundCode"`
	Status            string                 `json:"status"`
	Actor             string                 `json:"actor"`
	RequestID         string                 `json:"requestId"`
	Defect            CompletionDefectDetail `json:"defect"`
	CreatedAt         time.Time              `json:"createdAt"`
}

// ParseCheckDetails decodes the JSON verification snapshot persisted with every
// InspectionCompletionCheck.
func ParseCheckDetails(raw string) (CompletionCheckDetails, error) {
	var details CompletionCheckDetails
	if strings.TrimSpace(raw) == "" {
		return details, nil
	}
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		return CompletionCheckDetails{}, err
	}
	return details, nil
}
