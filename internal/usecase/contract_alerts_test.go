package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type fakeDetector struct {
	changes []domain.ContractChange
	err     error
}

func (f fakeDetector) BreakingChanges(_, _ string) ([]domain.ContractChange, error) {
	return f.changes, f.err
}

type recordingAlerts struct {
	types    []string
	payloads []usecase.BreakingChangesAlert
}

func (r *recordingAlerts) Notify(_ context.Context, eventType string, payload any) error {
	r.types = append(r.types, eventType)

	if a, ok := payload.(*usecase.BreakingChangesAlert); ok {
		r.payloads = append(r.payloads, *a)
	}

	return nil
}

func alertService(det fakeDetector, alerts usecase.AlertNotifier) (*usecase.ContractService, *mockContractRepo) {
	repo := newMockContractRepo()
	svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{}).
		WithChangeAlerts(det, alerts)

	return svc, repo
}

var breakingChange = domain.ContractChange{
	Kind: domain.ChangeFieldRemoved, Severity: domain.SeverityBreaking, Operation: "GET /users",
	Location: "response 200 application/json: name", Message: "field name was removed",
}

var warningChange = domain.ContractChange{
	Kind: domain.ChangeParamRemoved, Severity: domain.SeverityWarning, Operation: "GET /users",
	Location: "query parameter q", Message: "q was removed",
}

func TestContractAlerts_PublishBreakingSendsAlert(t *testing.T) {
	t.Parallel()

	alerts := &recordingAlerts{}
	svc, _ := alertService(fakeDetector{changes: []domain.ContractChange{warningChange, breakingChange}}, alerts)

	if _, err := svc.Publish("p1", "openapi: 3.0.0\n", "upload"); err != nil {
		t.Fatal(err)
	}

	if len(alerts.types) != 0 {
		t.Fatal("the first version has nothing to break")
	}

	if _, err := svc.Publish("p1", "openapi: 3.0.1\n", "upload"); err != nil {
		t.Fatal(err)
	}

	if len(alerts.payloads) != 1 || alerts.types[0] != usecase.EventBreakingChanges {
		t.Fatalf("want one breaking-changes alert, got %v", alerts.types)
	}

	a := alerts.payloads[0]
	if a.FromVersion != 1 || a.ToVersion != 2 || a.Breaking != 1 || a.Warnings != 1 || a.ProjectID != "p1" {
		t.Errorf("alert: %+v", a)
	}

	if a.Changes[0].Severity != domain.SeverityBreaking {
		t.Errorf("breaking changes come first: %+v", a.Changes)
	}

	if a.Summary() == "" {
		t.Error("an alert needs a one-line summary")
	}
}

func TestContractAlerts_WarningsOnlyAndErrorsStaySilent(t *testing.T) {
	t.Parallel()

	for name, det := range map[string]fakeDetector{
		"warnings only":  {changes: []domain.ContractChange{warningChange}, err: nil},
		"no changes":     {changes: nil, err: nil},
		"detector fails": {changes: nil, err: errors.New("boom")},
	} {
		alerts := &recordingAlerts{}
		svc, _ := alertService(det, alerts)

		_, _ = svc.Publish("p1", "openapi: 3.0.0\n", "upload")
		_, _ = svc.Publish("p1", "openapi: 3.0.1\n", "upload")

		if len(alerts.types) != 0 {
			t.Errorf("%s: no alert expected, got %v", name, alerts.types)
		}
	}
}

func TestContractAlerts_RollbackAlertsAndDiffListsChanges(t *testing.T) {
	t.Parallel()

	alerts := &recordingAlerts{}
	svc, _ := alertService(fakeDetector{changes: []domain.ContractChange{breakingChange}}, alerts)

	_, _ = svc.Publish("p1", "openapi: 3.0.0\n", "upload")
	_, _ = svc.Publish("p1", "openapi: 3.0.1\n", "upload")
	alerts.types, alerts.payloads = nil, nil

	rolled, err := svc.Rollback("p1", 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(alerts.payloads) != 1 || alerts.payloads[0].ToVersion != rolled.Version || alerts.payloads[0].Source != "rollback-to-v1" {
		t.Errorf("rollback must alert: %+v", alerts.payloads)
	}

	diff, err := svc.Diff("p1", 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(diff.Breaking) != 1 {
		t.Errorf("the diff must list the changes, got %+v", diff.Breaking)
	}
}

func TestContractAlerts_WithoutDetectorNothingChanges(t *testing.T) {
	t.Parallel()

	repo := newMockContractRepo()
	svc := usecase.NewContractService(repo, newMockProviderRepo(), &mockContractEngine{}, &mockLLMGateway{})

	_, _ = svc.Publish("p1", "openapi: 3.0.0\n", "upload")
	_, _ = svc.Publish("p1", "openapi: 3.0.1\n", "upload")

	diff, err := svc.Diff("p1", 1, nil)
	if err != nil || diff.Breaking == nil || len(diff.Breaking) != 0 {
		t.Errorf("want an empty, non-nil list: %+v %v", diff.Breaking, err)
	}
}
