package service

import (
	"errors"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
)

var (
	ErrInvalidTransition = errors.New("requested status transition is not allowed")
	ErrInvalidInput      = errors.New("business input validation failed")
	ErrUnauthorized      = errors.New("invalid username or password")
	ErrInactiveUser      = errors.New("user account is inactive")
	ErrDecisionLocked    = errors.New("final priority decisions are immutable")
	ErrReviewRole        = errors.New("reviewer or admin role is required to finalize a priority")
	ErrSeparationOfDuty  = errors.New("priority preparer cannot approve the same decision")
	ErrNotDecisionOwner  = errors.New("only the preparer may edit this draft decision")
)

// ErrCompletionBlocked signals that a batch cannot leave review because at
// least one linked defect failed the 缺陷处置核验. The round, defects, decisions
// and audit log are left untouched; the verification outcome (with blocking
// defect codes) is persisted separately for the UI to display.
type ErrCompletionBlocked struct {
	Message string
	Blockers []model.CompletionBlocker
}

func (e *ErrCompletionBlocked) Error() string { return e.Message }
func (e *ErrCompletionBlocked) Is(target error) bool {
	_, ok := target.(*ErrCompletionBlocked)
	return ok
}

