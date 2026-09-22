package repository

import (
	"strings"

	"github.com/blueship581/railway-bridge-defect-priority/backend/internal/model"
)

// defectJSONMarker matches the per-defect JSON object marker inside
// InspectionCompletionCheck.Details, for example "defectCode":"DF-001".
func defectJSONMarker(defectCode string) string {
	return `"defectCode":"` + strings.ToUpper(strings.TrimSpace(defectCode)) + `"`
}

// parseCheckDetails decodes the persisted verification snapshot.
func parseCheckDetails(raw string) (model.CompletionCheckDetails, error) {
	return model.ParseCheckDetails(raw)
}
