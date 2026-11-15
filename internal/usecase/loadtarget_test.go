package usecase_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func planFor(base string, allow ...string) *domain.Plan {
	p := new(domain.Plan)
	p.Target.BaseURL = base
	p.Target.AllowHosts = allow
	p.Load = domain.LoadModel{Kind: domain.LoadOpen, Stages: []domain.Stage{
		{Duration: domain.Duration(time.Minute), Rate: 10},
	}}

	return p
}

func TestResolveLoadTarget_AllowDeny(t *testing.T) {
	t.Parallel()

	caps := usecase.LoadCaps{}

	cases := []struct {
		name  string
		base  string
		allow []string
		role  domain.Role
		want  error
	}{
		{"exact host", "https://api.example.com/v1", []string{"api.example.com"}, domain.RoleAdmin, nil},
		{"exact host with port", "http://api.example.com:8080", []string{"api.example.com"}, domain.RoleLoadRunner, nil},
		{"case does not matter", "https://API.Example.com", []string{"api.EXAMPLE.com"}, domain.RoleAdmin, nil},
		{"wildcard", "https://eu.api.example.com", []string{"*.example.com"}, domain.RoleAdmin, nil},
		{"wildcard does not match the bare domain", "https://example.com", []string{"*.example.com"}, domain.RoleAdmin, usecase.ErrLoadTargetHost},
		{"wildcard does not match a look-alike", "https://evilexample.com", []string{"*.example.com"}, domain.RoleAdmin, usecase.ErrLoadTargetHost},
		{"other host", "https://other.com", []string{"api.example.com"}, domain.RoleAdmin, usecase.ErrLoadTargetHost},
		{"empty allow list", "https://api.example.com", nil, domain.RoleAdmin, usecase.ErrLoadTargetHost},
		{"empty entry matches nothing", "https://api.example.com", []string{"", "*."}, domain.RoleAdmin, usecase.ErrLoadTargetHost},
		{"editor may not use external targets", "https://api.example.com", []string{"api.example.com"}, domain.RoleEditor, usecase.ErrLoadTargetRole},
		{"viewer may not either", "https://api.example.com", []string{"api.example.com"}, domain.RoleViewer, usecase.ErrLoadTargetRole},
		{"credentials in the url", "https://u:p@api.example.com", []string{"api.example.com"}, domain.RoleAdmin, usecase.ErrLoadTargetURL},
		{"ftp scheme", "ftp://api.example.com", []string{"api.example.com"}, domain.RoleAdmin, usecase.ErrLoadTargetURL},
		{"not a url", "://", nil, domain.RoleAdmin, usecase.ErrLoadTargetURL},
		{"no host", "https:///x", nil, domain.RoleAdmin, usecase.ErrLoadTargetURL},
	}

	for _, tc := range cases {
		_, err := usecase.ResolveLoadTarget(planFor(tc.base, tc.allow...), tc.role, caps, "http://127.0.0.1:8080")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestResolveLoadTarget_Mock(t *testing.T) {
	t.Parallel()

	// Any role may load a mock: it is this server's own mock, no allow list.
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleEditor, domain.RoleLoadRunner} {
		got, err := usecase.ResolveLoadTarget(planFor("mock://proj-1"), role, usecase.LoadCaps{}, "http://127.0.0.1:8080/")
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}

		if !got.Mock || got.ProjectID != "proj-1" || got.BaseURL.String() != "http://127.0.0.1:8080/mock/proj-1" {
			t.Errorf("%s: %+v %v", role, got, got.BaseURL)
		}
	}

	if _, err := usecase.ResolveLoadTarget(planFor("mock://p"), domain.RoleAdmin, usecase.LoadCaps{}, ""); !errors.Is(err, usecase.ErrLoadNoMockBase) {
		t.Errorf("no server address: %v", err)
	}

	for _, bad := range []string{"mock://p/extra", "mock://p?x=1"} {
		if _, err := usecase.ResolveLoadTarget(planFor(bad), domain.RoleAdmin, usecase.LoadCaps{}, "http://h"); !errors.Is(err, usecase.ErrLoadTargetURL) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestResolveLoadTarget_Caps(t *testing.T) {
	t.Parallel()

	caps := usecase.LoadCaps{MaxRPS: 100, MaxVUs: 50, MaxDuration: 10 * time.Minute, MaxConns: 200}

	mutate := func(f func(p *domain.Plan)) *domain.Plan {
		p := planFor("mock://p")
		f(p)

		return p
	}

	cases := []struct {
		name string
		plan *domain.Plan
		ok   bool
	}{
		{"within caps", planFor("mock://p"), true},
		{"at the caps", mutate(func(p *domain.Plan) { p.Load.Stages[0].Rate = 100 }), true},
		{"rate above", mutate(func(p *domain.Plan) { p.Load.Stages[0].Rate = 101 }), false},
		{"vus above in stages", mutate(func(p *domain.Plan) {
			p.Load = domain.LoadModel{Kind: domain.LoadClosed, Stages: []domain.Stage{{Duration: domain.Duration(time.Minute), VUs: 51}}}
		}), false},
		{"vus above in iterations", mutate(func(p *domain.Plan) {
			p.Load = domain.LoadModel{Kind: domain.LoadIterations, VUs: 51, Iterations: 1}
		}), false},
		{"duration above", mutate(func(p *domain.Plan) { p.Load.Stages[0].Duration = domain.Duration(11 * time.Minute) }), false},
		{"conns above", mutate(func(p *domain.Plan) { p.Target.HTTP.MaxConnsPerHost = 201 }), false},
	}

	for _, tc := range cases {
		_, err := usecase.ResolveLoadTarget(tc.plan, domain.RoleAdmin, caps, "http://h")
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, usecase.ErrLoadCapExceeded)) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}

	// Zero caps mean no limit.
	huge := mutate(func(p *domain.Plan) { p.Load.Stages[0].Rate = 90_000 })
	if _, err := usecase.ResolveLoadTarget(huge, domain.RoleAdmin, usecase.LoadCaps{}, "http://h"); err != nil {
		t.Errorf("no caps: %v", err)
	}
}

func TestResolveLoadTarget_MaxConns(t *testing.T) {
	t.Parallel()

	caps := usecase.LoadCaps{MaxConns: 200}

	got, err := usecase.ResolveLoadTarget(planFor("mock://p"), domain.RoleAdmin, caps, "http://h")
	if err != nil || got.MaxConns != 200 {
		t.Errorf("an unset plan value takes the cap: %+v %v", got, err)
	}

	p := planFor("mock://p")
	p.Target.HTTP.MaxConnsPerHost = 20

	got, err = usecase.ResolveLoadTarget(p, domain.RoleAdmin, caps, "http://h")
	if err != nil || got.MaxConns != 20 {
		t.Errorf("a plan value under the cap is kept: %+v %v", got, err)
	}
}

func TestLoadCapsFromEnv(t *testing.T) {
	t.Parallel()

	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	def, err := usecase.LoadCapsFromEnv(env(nil))
	if err != nil || def.MaxRPS != usecase.DefaultLoadMaxRPS || def.MaxVUs != usecase.DefaultLoadMaxVUs ||
		def.MaxConns != usecase.DefaultLoadMaxConns || def.MaxDuration != usecase.DefaultLoadMaxDuration {
		t.Errorf("defaults: %+v %v", def, err)
	}

	got, err := usecase.LoadCapsFromEnv(env(map[string]string{
		"LOAD_MAX_RPS": "500", "LOAD_MAX_VUS": " 20 ", "LOAD_MAX_DURATION": "30m", "LOAD_MAX_CONNS": "0",
	}))
	if err != nil || got.MaxRPS != 500 || got.MaxVUs != 20 || got.MaxDuration != 30*time.Minute || got.MaxConns != 0 {
		t.Errorf("env: %+v %v", got, err)
	}

	for _, bad := range []map[string]string{
		{"LOAD_MAX_RPS": "lots"},
		{"LOAD_MAX_VUS": "-1"},
		{"LOAD_MAX_CONNS": "1.5"},
		{"LOAD_MAX_DURATION": "forever"},
		{"LOAD_MAX_DURATION": "-5m"},
	} {
		if _, err := usecase.LoadCapsFromEnv(env(bad)); !errors.Is(err, usecase.ErrLoadCapConfig) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestResolveLoadTarget_ServerPolicy(t *testing.T) {
	t.Parallel()

	guard := usecase.LoadCaps{BlockPrivate: true}

	cases := []struct {
		name      string
		base      string
		allow     []string
		caps      usecase.LoadCaps
		want      error
		wantGuard bool
	}{
		{"public name is guarded at connect", "https://api.example.com", []string{"api.example.com"}, guard, nil, true},
		{"public ip", "http://93.184.216.34", []string{"93.184.216.34"}, guard, nil, true},
		{"loopback", "http://127.0.0.1:9000", []string{"127.0.0.1"}, guard, usecase.ErrLoadTargetInternal, false},
		{"ipv6 loopback", "http://[::1]:8080", []string{"::1"}, guard, usecase.ErrLoadTargetInternal, false},
		{"localhost", "http://localhost:8080", []string{"localhost"}, guard, usecase.ErrLoadTargetInternal, false},
		{"private", "http://10.1.2.3", []string{"10.1.2.3"}, guard, usecase.ErrLoadTargetInternal, false},
		{"cloud metadata", "http://169.254.169.254", []string{"169.254.169.254"}, guard, usecase.ErrLoadTargetInternal, false},
		{"shared address space", "http://100.64.1.1", []string{"100.64.1.1"}, guard, usecase.ErrLoadTargetInternal, false},
		{"operator lifted the ban", "http://10.1.2.3", []string{"10.1.2.3"}, usecase.LoadCaps{}, nil, false},
		{
			"server allow list names the host", "http://staging.internal",
			[]string{"staging.internal"},
			usecase.LoadCaps{AllowHosts: []string{"*.internal"}, BlockPrivate: true},
			nil, false,
		},
		{
			"server allow list does not", "https://api.example.com",
			[]string{"api.example.com"},
			usecase.LoadCaps{AllowHosts: []string{"other.com"}},
			usecase.ErrLoadTargetServerHost, false,
		},
		{
			"a plan cannot widen the server list", "https://api.example.com",
			[]string{"*.com"},
			usecase.LoadCaps{AllowHosts: []string{"other.com"}},
			usecase.ErrLoadTargetServerHost, false,
		},
	}

	for _, tc := range cases {
		got, err := usecase.ResolveLoadTarget(planFor(tc.base, tc.allow...), domain.RoleLoadRunner, tc.caps, "http://127.0.0.1:8080")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)

			continue
		}

		if err == nil && got.BlockPrivate != tc.wantGuard {
			t.Errorf("%s: BlockPrivate = %v, want %v", tc.name, got.BlockPrivate, tc.wantGuard)
		}
	}

	mock, err := usecase.ResolveLoadTarget(planFor("mock://p"), domain.RoleEditor, guard, "http://127.0.0.1:8080")
	if err != nil || mock.BlockPrivate {
		t.Errorf("the server's own mock is loopback and must stay reachable: %+v %v", mock, err)
	}
}

func TestLoadCapsFromEnv_Guards(t *testing.T) {
	t.Parallel()

	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	def, err := usecase.LoadCapsFromEnv(env(nil))
	if err != nil || !def.BlockPrivate || len(def.AllowHosts) != 0 {
		t.Errorf("defaults: %+v %v", def, err)
	}

	got, err := usecase.LoadCapsFromEnv(env(map[string]string{
		"LOAD_ALLOW_HOSTS": " a.example.com, *.b.example.com ,, ", "LOAD_ALLOW_PRIVATE": "true",
	}))
	if err != nil || got.BlockPrivate || len(got.AllowHosts) != 2 || got.AllowHosts[1] != "*.b.example.com" {
		t.Errorf("env: %+v %v", got, err)
	}

	_, err = usecase.LoadCapsFromEnv(env(map[string]string{"LOAD_ALLOW_PRIVATE": "maybe"}))
	if !errors.Is(err, usecase.ErrLoadCapConfig) {
		t.Errorf("bad LOAD_ALLOW_PRIVATE: %v", err)
	}
}
