package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

const matchManifest = `{"mcpServer":{"name":"m","tools":[{"name":"weather",
 "mockResponses":{
  "default":{"content":[{"type":"text","text":"default"}]},
  "oslo":{"match":{"city":"Oslo"},"content":[{"type":"text","text":"oslo"}]},
  "oslo-c":{"match":{"city":"Oslo","units.temp":"c"},"content":[{"type":"text","text":"oslo-c"}]},
  "big":{"match":[{"name":"days","op":"regex","value":"^[1-9][0-9]$"}],"content":[{"type":"text","text":"big"}]},
  "rainy":{"content":[{"type":"text","text":"rainy"}]}
 }}]}}`

func textFor(t *testing.T, scenario, args string) string {
	t.Helper()

	out, err := NewEngine().Execute([]byte(matchManifest), callReq("weather", args), scenario)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var resp struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}

	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("bad response %s: %v", out, err)
	}

	if resp.Error != nil {
		return "ERR:" + resp.Error.Message
	}

	if strings.Contains(string(out), `"match"`) {
		t.Errorf("the match key must not leak into the result: %s", out)
	}

	return resp.Result.Content[0].Text
}

func TestMatchArguments_Selection(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, scenario, args, want string }{
		{"single condition", "", `{"city":"Oslo"}`, "oslo"},
		{"most conditions win", "", `{"city":"Oslo","units":{"temp":"c"}}`, "oslo-c"},
		{"array form with regex", "", `{"days":14}`, "big"},
		{"no match falls to default", "", `{"city":"Rome","days":3}`, "default"},
		{"missing arguments fall to default", "", ``, "default"},
		{"explicit scenario wins", "rainy", `{"city":"Oslo"}`, "rainy"},
		{"unknown scenario still matches", "nope", `{"city":"Oslo"}`, "oslo"},
	}

	for _, tc := range cases {
		if got := textFor(t, tc.scenario, tc.args); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMatchArguments_ConditionalOnlyToolDoesNotServeLoneResponse(t *testing.T) {
	t.Parallel()

	manifest := `{"mcpServer":{"name":"m","tools":[{"name":"t","mockResponses":{
		"only":{"match":{"a":"1"},"content":[{"type":"text","text":"only"}]}}}]}}`

	out, err := NewEngine().Execute([]byte(manifest), callReq("t", `{"a":"2"}`), "")
	if err != nil || !strings.Contains(string(out), `"error"`) {
		t.Errorf("an unmatched conditional response must not be served: %s (%v)", out, err)
	}

	out, _ = NewEngine().Execute([]byte(manifest), callReq("t", `{"a":"1"}`), "")
	if !strings.Contains(string(out), "only") {
		t.Errorf("matching call must be served: %s", out)
	}
}

func TestValidate_RejectsMalformedMatch(t *testing.T) {
	t.Parallel()

	for _, match := range []string{`"x"`, `[{"name":"a","op":"like"}]`, `[{"name":"a","op":"regex","value":"("}]`} {
		manifest := `{"mcpServer":{"name":"m","tools":[{"name":"t","mockResponses":{
			"r":{"match":` + match + `,"content":[]}}}]}}`

		if res := NewEngine().Validate([]byte(manifest)); res.Valid {
			t.Errorf("match %s must be rejected", match)
		}
	}
}
