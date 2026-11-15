package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const specV1 = `openapi: 3.0.0
info: {title: T, version: "1"}
paths:
  /users:
    get:
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  id: {type: integer}
                  name: {type: string}
`

func writeSpec(t *testing.T, dir, name, body string) string {
	t.Helper()

	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestRunDiff_ExitCodes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	oldP := writeSpec(t, dir, "old.yaml", specV1)
	same := writeSpec(t, dir, "same.yaml", specV1)
	breaking := writeSpec(t, dir, "breaking.yaml", strings.Replace(specV1, "                  name: {type: string}\n", "", 1))
	broken := writeSpec(t, dir, "broken.yaml", "not: [valid")

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"identical", []string{oldP, same, "--fail-on-breaking"}, 0, "No changes"},
		{"breaking without the flag only reports", []string{oldP, breaking}, 0, "BREAKING"},
		{"breaking with the flag after the files", []string{oldP, breaking, "--fail-on-breaking"}, 1, "field name was removed"},
		{"breaking with the flag before the files", []string{"--fail-on-breaking", oldP, breaking}, 1, "1 breaking change(s)"},
		{"unreadable contract", []string{oldP, broken}, 2, ""},
		{"missing file", []string{oldP, filepath.Join(dir, "nope.yaml")}, 2, ""},
		{"one file only", []string{oldP}, 2, ""},
		{"unknown flag", []string{oldP, same, "--nope"}, 2, ""},
		{"help", []string{"--help"}, 0, "Usage: openapi-mocker diff"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			if code := runDiff(tc.args, &stdout, &stderr); code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout.String(), stderr.String())
			}

			if !strings.Contains(stdout.String(), tc.wantOut) {
				t.Errorf("stdout %q lacks %q", stdout.String(), tc.wantOut)
			}
		})
	}
}

func TestRunDiff_JSONFormat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	oldP := writeSpec(t, dir, "old.yaml", specV1)
	newP := writeSpec(t, dir, "new.yaml", strings.Replace(specV1, "                  name: {type: string}\n", "", 1))

	var stdout, stderr bytes.Buffer

	if code := runDiff([]string{oldP, newP, "--format", "json", "--fail-on-breaking"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}

	var out struct {
		Breaking int              `json:"breaking"`
		Changes  []map[string]any `json:"changes"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout.String())
	}

	if out.Breaking != 1 || len(out.Changes) != 1 || out.Changes[0]["kind"] != "field-removed" {
		t.Errorf("result: %+v", out)
	}
}
