package openapi_test

import (
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
)

const doc31 = `{"openapi":"3.1.0","info":{"title":"T","version":"1","summary":"x"},"webhooks":{"w":{}},
"paths":{"/x":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{
 "type":["integer","null"],"exclusiveMinimum":0,"const":5,"prefixItems":[{}]}}}}}}}}}`

func TestNormalize_Passthrough(t *testing.T) {
	t.Parallel()

	raw := `{"openapi":"3.0.3","info":{"title":"T","version":"1"},"paths":{}}`

	out, note, err := openapi.NewEngine().Normalize(raw)
	if err != nil || out != raw || note != "" {
		t.Fatalf("3.0 must pass through untouched: %q %q %v", out, note, err)
	}
}

func TestNormalize_31Downgrade(t *testing.T) {
	t.Parallel()

	out, note, err := openapi.NewEngine().Normalize(doc31)
	if err != nil {
		t.Fatal(err)
	}

	for _, gone := range []string{"webhooks", "prefixItems", `"const"`, "summary"} {
		if strings.Contains(out, gone) {
			t.Errorf("%s should be removed:\n%s", gone, out)
		}
	}

	for _, want := range []string{`"3.0.3"`, `"nullable": true`, `"exclusiveMinimum": true`, `"minimum": 0`, `"enum"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}

	if !strings.Contains(note, "prefixItems") {
		t.Errorf("the note must name dropped keywords: %q", note)
	}

	if res := openapi.NewEngine().Validate([]byte(out)); !res.Valid {
		t.Errorf("downgraded contract must be valid: %+v", res)
	}
}

func TestExport_RoundTripTargets(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()
	v30, _, _ := e.Normalize(doc31)

	for _, target := range []string{"openapi3", "openapi31", "swagger2"} {
		for _, format := range []string{"json", "yaml"} {
			out, err := e.Export(v30, target, format)
			if err != nil || strings.TrimSpace(out) == "" {
				t.Errorf("%s/%s: %q %v", target, format, out, err)
			}
		}
	}

	if _, err := e.Export(v30, "raml", "json"); err == nil {
		t.Error("an unknown target must fail")
	}
}
