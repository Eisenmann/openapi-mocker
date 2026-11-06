package mcp

import (
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const faultManifest = `{"mcpServer":{"name":"m","tools":[
 {"name":"plain","mockResponses":{"default":{"content":[{"type":"text","text":"plain"}]}}},
 {"name":"flaky","fault":{"delayMs":100,"jitterMs":10,"errorRatePct":30,"dropRatePct":20},
  "mockResponses":{
   "default":{"content":[{"type":"text","text":"flaky"}]},
   "slow":{"fault":{"delayMs":900,"malformedRatePct":50,"malformedMode":"empty"},"content":[{"type":"text","text":"slow"}]},
   "vip":{"match":{"tier":"vip"},"content":[{"type":"text","text":"vip"}]}}}]}}`

// run executes one call and records the faults the engine asked to roll.
func run(t *testing.T, tool, args, scenario string, answer string) (out string, rolled []*usecase.MCPFault) {
	t.Helper()

	opts := &usecase.MCPExecOptions{
		Scenario: scenario,
		Fault: func(f *usecase.MCPFault) string {
			rolled = append(rolled, f)

			return answer
		},
	}

	res, _, err := NewEngine().ExecuteWith([]byte(faultManifest), callReq(tool, args), opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	return string(res), rolled
}

func TestFault_ToolLevelAndEntryOverride(t *testing.T) {
	t.Parallel()

	out, rolled := run(t, "plain", "", "", "")
	if len(rolled) != 0 || !strings.Contains(out, "plain") {
		t.Errorf("a tool without a fault must not roll one: %s %v", out, rolled)
	}

	out, rolled = run(t, "flaky", "", "", "")
	if len(rolled) != 1 || rolled[0].DelayMs != 100 || rolled[0].ErrorRatePct != 30 ||
		rolled[0].Settings.DropRatePct != 20 || rolled[0].Settings.JitterMs != 10 {
		t.Fatalf("the tool's fault applies to its default response: %+v", rolled)
	}

	if !strings.Contains(out, "flaky") || strings.Contains(out, `"fault"`) {
		t.Errorf("the response is served without the fault key: %s", out)
	}

	// An entry's own fault replaces the tool's.
	out, rolled = run(t, "flaky", "", "slow", "")
	if len(rolled) != 1 || rolled[0].DelayMs != 900 || rolled[0].Settings.DropRatePct != 0 ||
		rolled[0].Settings.MalformedRatePct != 50 {
		t.Fatalf("the entry's fault replaces the tool's: %+v", rolled)
	}

	if !strings.Contains(out, "slow") || strings.Contains(out, `"fault"`) {
		t.Errorf("entry served without the fault key: %s", out)
	}

	// A matched entry without a fault inherits the tool's.
	_, rolled = run(t, "flaky", `{"tier":"vip"}`, "", "")
	if len(rolled) != 1 || rolled[0].DelayMs != 100 {
		t.Errorf("entries inherit the tool's fault: %+v", rolled)
	}
}

func TestFault_ErrorAnswersWithJSONRPCError(t *testing.T) {
	t.Parallel()

	out, _ := run(t, "flaky", "", "", usecase.FaultError)
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "injected failure") || strings.Contains(out, `"flaky"`) {
		t.Errorf("an injected error must replace the result: %s", out)
	}

	// Other faults are applied by the caller; the engine still answers.
	out, _ = run(t, "flaky", "", "", usecase.FaultDrop)
	if !strings.Contains(out, "flaky") {
		t.Errorf("a connection-level fault leaves the response to the caller: %s", out)
	}
}

func TestValidate_RejectsMalformedFault(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"rates over 100":    `{"errorRatePct":80,"dropRatePct":30}`,
		"unknown field":     `{"dropRate":10}`,
		"not an object":     `"slow"`,
		"bad status":        `{"errorRatePct":10,"errorStatuses":[200]}`,
		"bad malformedMode": `{"malformedRatePct":10,"malformedMode":"weird"}`,
		"negative delay":    `{"delayMs":-5}`,
	}

	for name, fault := range cases {
		onTool := `{"mcpServer":{"name":"m","tools":[{"name":"t","fault":` + fault +
			`,"mockResponses":{"default":{"content":[]}}}]}}`
		onEntry := `{"mcpServer":{"name":"m","tools":[{"name":"t","mockResponses":{"default":{"fault":` +
			fault + `,"content":[]}}}]}}`

		for where, manifest := range map[string]string{"tool": onTool, "entry": onEntry} {
			if res := NewEngine().Validate([]byte(manifest)); res.Valid {
				t.Errorf("%s on %s must be rejected", name, where)
			}
		}
	}

	valid := `{"mcpServer":{"name":"m","tools":[{"name":"t","fault":{"failRatePct":10,"errorStatuses":[429]},
		"mockResponses":{"default":{"content":[]}}}]}}`
	if res := NewEngine().Validate([]byte(valid)); !res.Valid {
		t.Errorf("failRatePct is an accepted alias: %+v", res)
	}
}
