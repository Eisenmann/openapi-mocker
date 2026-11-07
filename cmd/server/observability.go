package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// capturePolicyFromEnv reads LOG_CAPTURE_BODIES (default on; "false", "0",
// "off" or "no" turn it off) and LOG_CAPTURE_MAX_BYTES (default 16384, at
// most 262144).
func capturePolicyFromEnv() usecase.CapturePolicy {
	return capturePolicy(os.Getenv("LOG_CAPTURE_BODIES"), os.Getenv("LOG_CAPTURE_MAX_BYTES"))
}

func capturePolicy(enabled, maxBytes string) usecase.CapturePolicy {
	p := usecase.DefaultCapturePolicy()

	switch strings.ToLower(strings.TrimSpace(enabled)) {
	case "false", "0", "off", "no":
		p.Enabled = false
	}

	n, err := strconv.Atoi(strings.TrimSpace(maxBytes))
	if err == nil && n > 0 {
		p.MaxBytes = min(n, usecase.MaxCaptureBytes)
	}

	return p
}
