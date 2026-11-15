package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	build := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: v}}
	}

	tests := []struct {
		name     string
		injected string
		info     *debug.BuildInfo
		want     string
	}{
		{"release build keeps the injected version", "1.2.3", build("v9.9.9"), "1.2.3"},
		{"go install uses the module version", "dev", build("v1.4.0"), "1.4.0"},
		{"pre-release module version", "dev", build("v1.4.0-rc.1"), "1.4.0-rc.1"},
		{
			"pseudo-version from go install @latest", "dev", build("v0.0.0-20261115120000-abcdef123456"),
			"0.0.0-20261115120000-abcdef123456",
		},
		{"local build", "dev", build("(devel)"), "dev"},
		{"no module version", "dev", build(""), "dev"},
		{"no build info", "dev", nil, "dev"},
		{"empty injected value", "", nil, "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.injected, tt.info); got != tt.want {
				t.Errorf("resolveVersion(%q) = %q, want %q", tt.injected, got, tt.want)
			}
		})
	}
}
