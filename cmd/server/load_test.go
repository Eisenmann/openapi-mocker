package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const iterationsPlan = `{
  "name": "cli test",
  "target": {"baseUrl": "http://placeholder.invalid"},
  "scenarios": {"ping": {"weight": 1, "steps": [
    {"name": "ping", "method": "GET", "path": "/ping", "checks": ["status == 200"]}
  ]}},
  "load": {"model": "iterations", "iterations": 20, "vus": 2},
  "thresholds": ["errors.rate < 1%"]
}`

func writePlan(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "plan.json")

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoadValidate(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer

	if code := runLoad([]string{"validate", writePlan(t, iterationsPlan)}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}

	if !strings.Contains(out.String(), "ok") {
		t.Errorf("output: %q", out.String())
	}

	out.Reset()
	errOut.Reset()

	if code := runLoad([]string{"validate", writePlan(t, `{"name": "x"}`)}, &out, &errOut); code != exitUsage {
		t.Errorf("invalid plan exit %d", code)
	}
}

func TestLoadRun_PassWritesReport(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "report")

	var out, errOut bytes.Buffer

	args := []string{
		"run", writePlan(t, iterationsPlan), "--target", srv.URL, "--out", dir, "--format", "html,json,junit,csv", "--quiet",
	}

	if code := runLoad(args, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}

	for _, name := range []string{"report.html", "report.json", "junit.xml", "timeseries.csv"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() == 0 {
			t.Errorf("%s missing or empty: %v", name, err)
		}
	}

	if !strings.Contains(out.String(), "20 requests") || !strings.Contains(out.String(), "PASS") {
		t.Errorf("summary: %q", out.String())
	}
}

func TestLoadRun_FailingThresholdExitsOne(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer

	args := []string{
		"run", writePlan(t, iterationsPlan), "--target", srv.URL, "--out", t.TempDir(), "--quiet",
	}

	if code := runLoad(args, &out, &errOut); code != 1 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
	}

	if !strings.Contains(out.String(), "FAIL") {
		t.Errorf("summary: %q", out.String())
	}
}

func TestLoadRun_ExternalTargetNeedsAllowance(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer

	// No --target: the plan's own host is not in allowHosts.
	code := runLoad([]string{"run", writePlan(t, iterationsPlan), "--out", t.TempDir(), "--quiet"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "placeholder.invalid") {
		t.Errorf("exit %d: %s", code, errOut.String())
	}
}

func TestLoadRun_UsageErrors(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{},
		{"nope"},
		{"run"},
		{"run", "a.json", "b.json"},
		{"run", "plan.json", "--format", "pdf"},
		{"generate"},
		{"generate", "--project", "p", "c.yaml"},
	} {
		var out, errOut bytes.Buffer

		if code := runLoad(args, &out, &errOut); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

const petsContract = `openapi: 3.0.3
info: {title: pets, version: "1"}
paths:
  /pets:
    get:
      operationId: listPets
      responses: {"200": {description: ok}}
`

func TestLoadGenerate_WritesParsablePlan(t *testing.T) {
	t.Parallel()

	contract := filepath.Join(t.TempDir(), "pets.yaml")
	if err := os.WriteFile(contract, []byte(petsContract), 0o600); err != nil {
		t.Fatal(err)
	}

	planPath := filepath.Join(t.TempDir(), "plan.json")

	var out, errOut bytes.Buffer

	args := []string{"generate", contract, "--format", "json", "--target", "http://localhost:9", "-o", planPath}
	if code := runLoad(args, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}

	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := usecase.ParsePlan(raw)
	if err != nil {
		t.Fatalf("generated plan must parse: %v", err)
	}

	if err := plan.Validate(); err != nil {
		t.Errorf("generated plan must validate: %v", err)
	}
}
