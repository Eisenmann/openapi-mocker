package domain_test

import (
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func TestMockRule_ValidateFault(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		rule domain.MockRule
		want error
	}{
		{"no fault", domain.MockRule{}, nil},
		{"latency only", domain.MockRule{DelayMs: 200, Fault: &domain.Fault{JitterMs: 50}}, nil},
		{"all outcomes at 100 in total", domain.MockRule{FailRatePct: 25, Fault: &domain.Fault{
			DropRatePct: 25, TimeoutRatePct: 25, MalformedRatePct: 25, MalformedMode: domain.MalformedTruncated,
			ErrorStatuses: []int{429, 503}, TimeoutMs: 5000,
		}}, nil},
		{"negative delay", domain.MockRule{DelayMs: -1}, domain.ErrFaultLatency},
		{"delay too long", domain.MockRule{DelayMs: domain.MaxLatencyMs + 1}, domain.ErrFaultLatency},
		{"jitter too long", domain.MockRule{Fault: &domain.Fault{JitterMs: domain.MaxLatencyMs + 1}}, domain.ErrFaultLatency},
		{"rate above 100", domain.MockRule{FailRatePct: 101}, domain.ErrFaultRate},
		{"negative rate", domain.MockRule{Fault: &domain.Fault{DropRatePct: -5}}, domain.ErrFaultRate},
		{"rates add up past 100", domain.MockRule{FailRatePct: 60, Fault: &domain.Fault{DropRatePct: 41}}, domain.ErrFaultRateTotal},
		{"status below 400", domain.MockRule{Fault: &domain.Fault{ErrorStatuses: []int{200}}}, domain.ErrFaultStatus},
		{"status above 599", domain.MockRule{Fault: &domain.Fault{ErrorStatuses: []int{600}}}, domain.ErrFaultStatus},
		{"timeout too long", domain.MockRule{Fault: &domain.Fault{TimeoutMs: domain.MaxTimeoutMs + 1}}, domain.ErrFaultTimeout},
		{"unknown malformed mode", domain.MockRule{Fault: &domain.Fault{MalformedMode: "weird"}}, domain.ErrFaultMode},
	}

	for _, tc := range cases {
		if err := tc.rule.ValidateFault(); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestFault_IsZeroAndMode(t *testing.T) {
	t.Parallel()

	var nilFault *domain.Fault

	if !nilFault.IsZero() || !(&domain.Fault{}).IsZero() {
		t.Error("nil and empty faults do nothing")
	}

	if (&domain.Fault{DropRatePct: 1}).IsZero() {
		t.Error("a fault with a rate is not zero")
	}

	if nilFault.Mode() != domain.MalformedGarbage || (&domain.Fault{}).Mode() != domain.MalformedGarbage {
		t.Error("the default malformed mode is garbage")
	}

	if (&domain.Fault{MalformedMode: domain.MalformedEmpty}).Mode() != domain.MalformedEmpty {
		t.Error("an explicit mode must be kept")
	}
}
