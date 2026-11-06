package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	// EventBreakingChanges is the alert event sent when a new contract version
	// breaks the previous one.
	EventBreakingChanges = "contract.breaking_changes"

	maxAlertChanges = 50
	alertTimeout    = 15 * time.Second
)

// BreakingChangesAlert is the payload of EventBreakingChanges.
type BreakingChangesAlert struct {
	ProjectID   string `json:"projectId"`
	FromVersion int    `json:"fromVersion"`
	ToVersion   int    `json:"toVersion"`
	Source      string `json:"source"`
	Breaking    int    `json:"breaking"`
	Warnings    int    `json:"warnings"`
	// Changes holds the breaking changes first, capped at 50.
	Changes   []domain.ContractChange `json:"changes"`
	Truncated bool                    `json:"truncated,omitempty"`
}

// changes lists what differs between two versions for clients. It returns an
// empty list when no detector is wired, a version is not OpenAPI or cannot be
// parsed: the line diff is still shown in those cases.
func (s *ContractService) changes(from, to *domain.Contract) []domain.ContractChange {
	none := []domain.ContractChange{}

	if s.detector == nil || from == nil || to == nil || !isOpenAPIFormat(from.Format) || !isOpenAPIFormat(to.Format) {
		return none
	}

	changes, err := s.detector.BreakingChanges(from.Raw, to.Raw)
	if err != nil || changes == nil {
		return none
	}

	return changes
}

func isOpenAPIFormat(format string) bool {
	return format == FormatYAML || format == FormatJSON
}

// alertOnBreaking sends an alert when added breaks prev. Alerting is best
// effort: a failing notifier never fails publishing.
func (s *ContractService) alertOnBreaking(prev, added *domain.Contract) {
	if s.alerts == nil || prev == nil || added == nil {
		return
	}

	all := s.changes(prev, added)

	breaking := domain.CountBreaking(all)
	if breaking == 0 {
		return
	}

	alert := BreakingChangesAlert{
		ProjectID: added.ProjectID, FromVersion: prev.Version, ToVersion: added.Version, Source: added.Source,
		Breaking: breaking, Warnings: len(all) - breaking, Changes: breakingFirst(all), Truncated: false,
	}

	if len(alert.Changes) > maxAlertChanges {
		alert.Changes, alert.Truncated = alert.Changes[:maxAlertChanges], true
	}

	ctx, cancel := context.WithTimeout(context.Background(), alertTimeout)
	defer cancel()

	_ = s.alerts.Notify(ctx, EventBreakingChanges, &alert)
}

// breakingFirst returns the changes with the breaking ones in front.
func breakingFirst(changes []domain.ContractChange) []domain.ContractChange {
	out := make([]domain.ContractChange, 0, len(changes))

	for _, severity := range []domain.ChangeSeverity{domain.SeverityBreaking, domain.SeverityWarning} {
		for i := range changes {
			if changes[i].Severity == severity {
				out = append(out, changes[i])
			}
		}
	}

	return out
}

// Summary is a one-line description of the alert.
func (a *BreakingChangesAlert) Summary() string {
	return fmt.Sprintf("Contract of project %s: version %d breaks version %d (%d breaking change(s), %d warning(s))",
		a.ProjectID, a.ToVersion, a.FromVersion, a.Breaking, a.Warnings)
}
