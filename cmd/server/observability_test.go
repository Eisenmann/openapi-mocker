package main

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestCapturePolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		enabled, size string
		wantOn        bool
		wantMax       int
	}{
		{"", "", true, usecase.DefaultCaptureBytes},
		{"off", "", false, usecase.DefaultCaptureBytes},
		{"FALSE", "4096", false, 4096},
		{"true", "999999999", true, usecase.MaxCaptureBytes},
		{"", "-5", true, usecase.DefaultCaptureBytes},
		{"", "abc", true, usecase.DefaultCaptureBytes},
	}

	for _, c := range cases {
		got := capturePolicy(c.enabled, c.size)
		if got.Enabled != c.wantOn || got.MaxBytes != c.wantMax {
			t.Errorf("capturePolicy(%q,%q) = %+v", c.enabled, c.size, got)
		}
	}
}
