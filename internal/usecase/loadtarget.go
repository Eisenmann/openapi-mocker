package usecase

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// Defaults of the server-wide load caps, used when the environment does not
// set them.
const (
	DefaultLoadMaxRPS      = 1000
	DefaultLoadMaxVUs      = 1000
	DefaultLoadMaxDuration = time.Hour
	DefaultLoadMaxConns    = 1000

	mockScheme = "mock"
	mockPrefix = "/mock/"
)

// Environment variables that set the load caps.
const (
	EnvLoadMaxRPS      = "LOAD_MAX_RPS"
	EnvLoadMaxVUs      = "LOAD_MAX_VUS"
	EnvLoadMaxDuration = "LOAD_MAX_DURATION"
	EnvLoadMaxConns    = "LOAD_MAX_CONNS"
	// EnvLoadAllowHosts lists the hosts external targets may have, for the whole
	// server (comma-separated, the patterns of a plan's allowHosts).
	EnvLoadAllowHosts = "LOAD_ALLOW_HOSTS"
	// EnvLoadAllowPrivate set to true lets plans load private, loopback and
	// link-local addresses.
	EnvLoadAllowPrivate = "LOAD_ALLOW_PRIVATE"
)

// Errors returned when a plan's target or size is refused.
var (
	ErrLoadTargetURL        = errors.New("target.baseUrl must be mock://<projectId> or an http(s) URL without credentials")
	ErrLoadTargetHost       = errors.New("target host is not in target.allowHosts")
	ErrLoadTargetServerHost = errors.New("target host is not allowed by the server (LOAD_ALLOW_HOSTS)")
	ErrLoadTargetInternal   = errors.New(
		"target is a private, loopback or link-local address; the server operator must allow it " +
			"(LOAD_ALLOW_HOSTS or LOAD_ALLOW_PRIVATE=true)")
	ErrLoadTargetRole  = errors.New("load against an external target needs the admin or load-runner role")
	ErrLoadNoMockBase  = errors.New("the server's own address is unknown, mock:// targets are unavailable")
	ErrLoadCapExceeded = errors.New("plan exceeds a server load limit")
	ErrLoadCapConfig   = errors.New("invalid load limit in the environment")
)

// LoadCaps are the server-wide limits a plan must stay under. A zero field
// means "no limit".
type LoadCaps struct {
	MaxRPS      int
	MaxVUs      int
	MaxDuration time.Duration
	MaxConns    int
	// AllowHosts, when not empty, limits external targets to these hosts (an
	// exact name or *.suffix); a plan's own allowHosts can only narrow it.
	AllowHosts []string
	// BlockPrivate refuses external targets at private, loopback, link-local
	// and shared (CGNAT) addresses, also when a name resolves to one. It is
	// lifted by AllowHosts: the operator named the hosts.
	BlockPrivate bool
}

// LoadCapsFromEnv reads LOAD_MAX_RPS, LOAD_MAX_VUS, LOAD_MAX_DURATION (such as
// 30m) and LOAD_MAX_CONNS through getenv. Unset variables get the defaults
// above; 0 switches a cap off.
func LoadCapsFromEnv(getenv func(string) string) (LoadCaps, error) {
	var c LoadCaps

	var err error

	c.MaxRPS, err = envInt(getenv, EnvLoadMaxRPS, DefaultLoadMaxRPS)
	if err != nil {
		return c, err
	}

	c.MaxVUs, err = envInt(getenv, EnvLoadMaxVUs, DefaultLoadMaxVUs)
	if err != nil {
		return c, err
	}

	c.MaxConns, err = envInt(getenv, EnvLoadMaxConns, DefaultLoadMaxConns)
	if err != nil {
		return c, err
	}

	c.MaxDuration = DefaultLoadMaxDuration

	if raw := strings.TrimSpace(getenv(EnvLoadMaxDuration)); raw != "" {
		d, perr := time.ParseDuration(raw)
		if perr != nil || d < 0 {
			return c, fmt.Errorf("%w: %s=%q", ErrLoadCapConfig, EnvLoadMaxDuration, raw)
		}

		c.MaxDuration = d
	}

	err = guardsFromEnv(getenv, &c)

	return c, err
}

// guardsFromEnv reads LOAD_ALLOW_HOSTS and LOAD_ALLOW_PRIVATE. Private
// addresses are refused unless the operator says otherwise.
func guardsFromEnv(getenv func(string) string, c *LoadCaps) error {
	for _, h := range strings.Split(getenv(EnvLoadAllowHosts), ",") {
		if h = strings.TrimSpace(h); h != "" {
			c.AllowHosts = append(c.AllowHosts, h)
		}
	}

	c.BlockPrivate = true

	raw := strings.TrimSpace(getenv(EnvLoadAllowPrivate))
	if raw == "" {
		return nil
	}

	allow, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("%w: %s=%q", ErrLoadCapConfig, EnvLoadAllowPrivate, raw)
	}

	c.BlockPrivate = !allow

	return nil
}

func envInt(getenv func(string) string, name string, def int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return def, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s=%q", ErrLoadCapConfig, name, raw)
	}

	return n, nil
}

// LoadTarget is a plan's target after the safety checks.
type LoadTarget struct {
	// BaseURL is where requests go; for mock:// targets it points at this
	// server's mock of the project.
	BaseURL *url.URL
	// Mock is true for a mock://<projectId> target.
	Mock bool
	// ProjectID is the project of a mock target.
	ProjectID string
	// MaxConns is the connection limit to apply: the plan's own, or the cap
	// when the plan sets none (0 means unlimited).
	MaxConns int
	// BlockPrivate makes the engine refuse connections to private, loopback and
	// link-local addresses, checked on the resolved address when it connects.
	BlockPrivate bool
}

// ResolveLoadTarget decides whether a plan may run. Load is only sent where
// it is allowed:
//
//   - mock://<projectId> resolves to mockBase (this server's own address)
//     plus /mock/<projectId>;
//   - any other target must be http or https, carry no credentials, have a
//     host that matches target.allowHosts (an exact name or *.suffix) and be
//     run by an admin or a load-runner;
//   - rates, VUs, duration and connections above the caps are refused.
func ResolveLoadTarget(plan *domain.Plan, role domain.Role, caps LoadCaps, mockBase string) (*LoadTarget, error) {
	u, err := url.Parse(plan.Target.BaseURL)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%w (got %q)", ErrLoadTargetURL, plan.Target.BaseURL)
	}

	var t *LoadTarget

	if u.Scheme == mockScheme {
		t, err = resolveMock(u, mockBase)
	} else {
		t, err = resolveExternal(u, plan.Target.AllowHosts, role, &caps)
	}

	if err != nil {
		return nil, err
	}

	err = checkCaps(plan, caps)
	if err != nil {
		return nil, err
	}

	t.MaxConns = plan.Target.HTTP.MaxConnsPerHost
	if t.MaxConns == 0 {
		t.MaxConns = caps.MaxConns
	}

	return t, nil
}

func resolveMock(u *url.URL, mockBase string) (*LoadTarget, error) {
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, fmt.Errorf("%w (a mock:// target is just mock://<projectId>)", ErrLoadTargetURL)
	}

	base, err := url.Parse(strings.TrimRight(mockBase, "/"))
	if mockBase == "" || err != nil || base.Host == "" {
		return nil, ErrLoadNoMockBase
	}

	target := new(LoadTarget)
	target.Mock = true
	target.ProjectID = u.Host
	target.BaseURL = base.JoinPath(mockPrefix + u.Host)

	return target, nil
}

func resolveExternal(u *url.URL, allow []string, role domain.Role, caps *LoadCaps) (*LoadTarget, error) {
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w (scheme %q)", ErrLoadTargetURL, u.Scheme)
	}

	if role != domain.RoleAdmin && role != domain.RoleLoadRunner {
		return nil, ErrLoadTargetRole
	}

	if !hostAllowed(u.Hostname(), allow) {
		return nil, fmt.Errorf("%w (%s)", ErrLoadTargetHost, u.Hostname())
	}

	guard, err := checkServerPolicy(u.Hostname(), caps)
	if err != nil {
		return nil, err
	}

	target := new(LoadTarget)
	target.BaseURL = u
	target.BlockPrivate = guard

	return target, nil
}

// checkServerPolicy applies what the operator set for the whole server: the
// host allow list, and the ban on private addresses. It reports whether the
// connection must be guarded against private addresses.
func checkServerPolicy(host string, caps *LoadCaps) (guard bool, err error) {
	if len(caps.AllowHosts) > 0 {
		if !hostAllowed(host, caps.AllowHosts) {
			return false, fmt.Errorf("%w (%s)", ErrLoadTargetServerHost, host)
		}

		return false, nil // the operator named the hosts, inside or not.
	}

	if caps.BlockPrivate && internalHost(host) {
		return false, fmt.Errorf("%w (%s)", ErrLoadTargetInternal, host)
	}

	return caps.BlockPrivate, nil
}

// internalHost is true for localhost and for a literal private address.
func internalHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}

	bare, _, _ := strings.Cut(host, "%") // an IPv6 zone.
	ip := net.ParseIP(bare)

	return ip != nil && IsInternalIP(ip)
}

// IsInternalIP is true for loopback, private (RFC 1918, unique local),
// link-local (also the cloud metadata address), unspecified, multicast and
// shared CGNAT (100.64.0.0/10) addresses.
func IsInternalIP(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")

	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || cgnat.Contains(ip)
}

// hostAllowed reports whether host matches an exact entry or a *.suffix
// entry of the allow list. *.example.com matches api.example.com but not
// example.com itself. An empty list allows nothing.
func hostAllowed(host string, allow []string) bool {
	host = strings.ToLower(host)

	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))

		if suffix, wild := strings.CutPrefix(a, "*."); wild {
			if suffix != "" && strings.HasSuffix(host, "."+suffix) {
				return true
			}

			continue
		}

		if a != "" && a == host {
			return true
		}
	}

	return false
}

// checkCaps refuses plan values above the server's limits.
func checkCaps(plan *domain.Plan, caps LoadCaps) error {
	over := func(name string, value, limit int) error {
		if limit > 0 && value > limit {
			return fmt.Errorf("%w: %s %d is above %d", ErrLoadCapExceeded, name, value, limit)
		}

		return nil
	}

	var maxRate, maxVUs int

	for _, s := range plan.Load.Stages {
		maxRate = max(maxRate, s.Rate)
		maxVUs = max(maxVUs, s.VUs)
	}

	maxVUs = max(maxVUs, plan.Load.VUs)

	for _, err := range []error{
		over("rate", maxRate, caps.MaxRPS),
		over("vus", maxVUs, caps.MaxVUs),
		over("maxConnsPerHost", plan.Target.HTTP.MaxConnsPerHost, caps.MaxConns),
	} {
		if err != nil {
			return err
		}
	}

	if d := plan.Load.TotalDuration(); caps.MaxDuration > 0 && d > caps.MaxDuration {
		return fmt.Errorf("%w: duration %s is above %s", ErrLoadCapExceeded, d, caps.MaxDuration)
	}

	return nil
}
