package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const stdioManifest = `{"mcpServer":{"name":"stdio-demo",
	"tools":[{"name":"echo","title":"Echo","mockResponses":{"default":{"content":[{"type":"text","text":"pong"}]},
		"loud":{"content":[{"type":"text","text":"PONG"}]}}}],
	"resources":[{"uri":"mem://a","text":"alpha"}],
	"prompts":[{"name":"greet","arguments":[{"name":"who"}],"messages":[{"role":"user","content":"hi {{who}}"}]}]}}`

func writeManifest(t *testing.T, body string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "m.json")

	err := os.WriteFile(p, []byte(body), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func runStdio(t *testing.T, args []string, input string) (code int, out, errOut string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code = runMCPStdio(args, strings.NewReader(input), &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestMCPStdio_Session(t *testing.T) {
	path := writeManifest(t, stdioManifest)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		``,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"mem://a"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"prompts/get","params":{"name":"greet","arguments":{"who":"Ann"}}}`,
		`not json`,
	}, "\n")

	code, out, errOut := runStdio(t, []string{path}, input)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 6 { // the notification gets no answer.
		t.Fatalf("want 6 responses, got %d:\n%s", len(lines), out)
	}

	for i, want := range []string{`"protocolVersion"`, `"title":"Echo"`, `pong`, `alpha`, `hi Ann`, `-32700`} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d lacks %s: %s", i, want, lines[i])
		}

		if strings.Contains(lines[i], "\n") || !json.Valid([]byte(lines[i])) {
			t.Errorf("line %d is not one JSON message: %s", i, lines[i])
		}
	}

	if !strings.Contains(errOut, "serving stdio") || strings.Contains(out, "serving") {
		t.Errorf("diagnostics belong on stderr only: %q / %q", errOut, out)
	}
}

func TestMCPStdio_Scenario(t *testing.T) {
	path := writeManifest(t, stdioManifest)

	code, out, errOut := runStdio(t, []string{"--scenario", "loud", path},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`+"\n")
	if code != exitOK || !strings.Contains(out, "PONG") {
		t.Errorf("scenario: %d %s %s", code, out, errOut)
	}
}

func TestMCPStdio_UsageErrors(t *testing.T) {
	good := writeManifest(t, stdioManifest)
	bad := writeManifest(t, `{"mcpServer":{"name":"x"}}`)

	for name, args := range map[string][]string{
		"no source":        {},
		"both sources":     {good, "--project", "p"},
		"two files":        {good, good},
		"unknown flag":     {"--nope", good},
		"flag w/o value":   {good, "--scenario"},
		"project w/o val":  {"--project"},
		"missing file":     {filepath.Join(t.TempDir(), "absent.json")},
		"invalid manifest": {bad},
	} {
		code, _, errOut := runStdio(t, args, "")
		if code != exitUsage || errOut == "" {
			t.Errorf("%s: exit %d, stderr %q", name, code, errOut)
		}
	}
}

func TestMCPStdio_ReadFailure(t *testing.T) {
	path := writeManifest(t, stdioManifest)

	huge := strings.Repeat("x", maxStdioLine+10)

	code, _, errOut := runStdio(t, []string{path}, huge+"\n")
	if code != exitIOFailure || !strings.Contains(errOut, "read request") {
		t.Errorf("oversized line: %d %s", code, errOut)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestMCPStdio_WriteFailure(t *testing.T) {
	path := writeManifest(t, stdioManifest)

	var stderr bytes.Buffer

	code := runMCPStdio([]string{path}, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"),
		failWriter{}, &stderr)
	if code != exitIOFailure || !strings.Contains(stderr.String(), "write response") {
		t.Errorf("write failure: %d %s", code, stderr.String())
	}
}

func TestMCPStdio_Project(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)

	store, err := jsonstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	store.AddVersion("proj1", usecase.FormatMCP, stdioManifest, "manual")
	store.AddVersion("proj2", usecase.FormatJSON, `{}`, "manual")

	code, out, errOut := runStdio(t, []string{"--project", "proj1"}, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")
	if code != exitOK || !strings.Contains(out, `"echo"`) {
		t.Errorf("project mode: %d %s %s", code, out, errOut)
	}

	code, _, errOut = runStdio(t, []string{"--project", "proj2"}, "")
	if code != exitUsage || !strings.Contains(errOut, "not an MCP contract") {
		t.Errorf("non-MCP project: %d %s", code, errOut)
	}

	code, _, _ = runStdio(t, []string{"--project", "ghost"}, "")
	if code != exitUsage {
		t.Errorf("unknown project: %d", code)
	}
}

func TestMCPStdio_InternalErrorReply(t *testing.T) {
	if got := internalErrorReply([]byte(`{"id":7}`), os.ErrClosed); !strings.Contains(string(got), `"id":7`) ||
		!strings.Contains(string(got), "-32603") {
		t.Errorf("reply: %s", got)
	}

	if internalErrorReply([]byte(`{"method":"notifications/x"}`), os.ErrClosed) != nil ||
		internalErrorReply([]byte(`junk`), os.ErrClosed) != nil {
		t.Error("notifications and junk get no error reply")
	}
}
