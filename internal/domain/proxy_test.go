package domain_test

import (
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func TestProxyConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  domain.ProxyConfig
		want error
	}{
		{"zero config is off", domain.ProxyConfig{UpstreamURL: "", Mode: "", DriftCheck: false}, nil},
		{"url alone is fine", domain.ProxyConfig{UpstreamURL: "http://localhost:9000", Mode: domain.ProxyOff, DriftCheck: false}, nil},
		{"record needs url", domain.ProxyConfig{UpstreamURL: "", Mode: domain.ProxyRecord, DriftCheck: false}, domain.ErrUpstreamRequired},
		{"drift needs url", domain.ProxyConfig{UpstreamURL: " ", Mode: domain.ProxyOff, DriftCheck: true}, domain.ErrUpstreamRequired},
		{"unknown mode", domain.ProxyConfig{UpstreamURL: "http://a.b", Mode: "mirror", DriftCheck: false}, domain.ErrProxyMode},
		{"relative url", domain.ProxyConfig{UpstreamURL: "/v1", Mode: domain.ProxyForward, DriftCheck: false}, domain.ErrUpstreamURL},
		{"ftp scheme", domain.ProxyConfig{UpstreamURL: "ftp://a.b", Mode: domain.ProxyForward, DriftCheck: false}, domain.ErrUpstreamURL},
		{"credentials", domain.ProxyConfig{UpstreamURL: "https://u:p@a.b", Mode: domain.ProxyForward, DriftCheck: false}, domain.ErrUpstreamURL},
		{"https ok", domain.ProxyConfig{UpstreamURL: "https://a.b/v1", Mode: domain.ProxyRecord, DriftCheck: true}, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.cfg.Validate(); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}
