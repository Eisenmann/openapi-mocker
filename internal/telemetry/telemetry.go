// Package telemetry is the opt-in anonymous usage ping. Nothing is sent unless
// the user turned it on, and what is sent is exactly the Payload below: no
// contract content, no paths, no host names, no addresses of the machine.
// docs/privacy.md describes it for users.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// SchemaVersion is the version of the Payload layout. It changes when a field
// is added or changes meaning.
const SchemaVersion = 1

const (
	// ModeServe is the "serve" command: one contract, no UI.
	ModeServe = "serve"
	// ModeServer is the full server with projects and the web UI.
	ModeServer = "server"
)

const (
	envSwitch   = "OPENAPI_MOCKER_TELEMETRY"
	envEndpoint = "OPENAPI_MOCKER_TELEMETRY_URL"
	envConfig   = "OPENAPI_MOCKER_CONFIG_DIR"
	envDNT      = "DO_NOT_TRACK"

	stateFile = "telemetry.json"
	appDir    = "openapi-mocker"

	idBytes = 16
	dirPerm = 0o755
	// filePerm keeps the state readable by the owner only.
	filePerm = 0o600

	sendTimeout = 5 * time.Second
	// Interval is how often a running process pings again, so that one
	// long-running server counts once per day.
	Interval = 24 * time.Hour
)

// Where a setting came from, for the status command.
const (
	SourceDefault     = "default, until you turn it on"
	SourceFile        = "your choice, saved in " + stateFile
	SourceEnvironment = "the " + envSwitch + " environment variable"
	SourceDoNotTrack  = "the " + envDNT + " environment variable"
)

// Payload is everything that is sent, as JSON.
type Payload struct {
	// Schema is SchemaVersion.
	Schema int `json:"schema"`
	// InstallID is a random identifier made when telemetry is turned on. It is
	// not derived from the machine or the user, and turning telemetry off
	// deletes it.
	InstallID string `json:"install_id"`
	// Version is the OpenAPI Mocker version.
	Version string `json:"version"`
	// OS and Arch are the Go runtime values, for example "linux" and "arm64".
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// Mode is ModeServe or ModeServer.
	Mode string `json:"mode"`
	// Protocols lists the kinds of contract being served: "openapi", "graphql",
	// "mcp" or "grpc".
	Protocols []string `json:"protocols"`
}

// Settings is the effective telemetry setting.
type Settings struct {
	Enabled bool
	// Source says why Enabled has its value.
	Source string
	// InstallID is empty when telemetry is off.
	InstallID string
	// Decided is true once the user has chosen either way (in the state file
	// or an environment variable).
	Decided bool
}

type state struct {
	Enabled   bool   `json:"enabled"`
	InstallID string `json:"install_id,omitempty"`
}

// Dir returns the directory that holds the state file: OPENAPI_MOCKER_CONFIG_DIR,
// else the user's configuration directory, else fallback (which may be "").
func Dir(getenv func(string) string, fallback string) string {
	if dir := getenv(envConfig); dir != "" {
		return dir
	}

	cfg, err := os.UserConfigDir()
	if err == nil && cfg != "" {
		return filepath.Join(cfg, appDir)
	}

	return fallback
}

// Load returns the effective setting. DO_NOT_TRACK=1 always wins; then the
// OPENAPI_MOCKER_TELEMETRY variable; then the saved choice; the default is off.
func Load(getenv func(string) string, dir string) Settings {
	saved := readState(dir)

	if truthy(getenv(envDNT)) {
		return Settings{Enabled: false, Source: SourceDoNotTrack, InstallID: "", Decided: true}
	}

	switch strings.ToLower(strings.TrimSpace(getenv(envSwitch))) {
	case "1", "true", "on", "yes":
		return Settings{Enabled: true, Source: SourceEnvironment, InstallID: installID(saved), Decided: true}
	case "0", "false", "off", "no":
		return Settings{Enabled: false, Source: SourceEnvironment, InstallID: "", Decided: true}
	}

	if saved.Enabled {
		return Settings{Enabled: true, Source: SourceFile, InstallID: installID(saved), Decided: true}
	}

	// A saved "disabled" is an explicit choice; no file at all is not.
	_, err := os.Stat(statePath(dir))

	return Settings{Enabled: false, Source: SourceDefault, InstallID: "", Decided: err == nil && dir != ""}
}

// Enable turns telemetry on and makes the install ID if there is none.
func Enable(dir string) (Settings, error) {
	if dir == "" {
		return Settings{}, errNoDir
	}

	st := readState(dir)
	st.Enabled = true

	if st.InstallID == "" {
		id, err := newID()
		if err != nil {
			return Settings{}, err
		}

		st.InstallID = id
	}

	err := writeState(dir, st)
	if err != nil {
		return Settings{}, err
	}

	return Settings{Enabled: true, Source: SourceFile, InstallID: st.InstallID, Decided: true}, nil
}

// Disable turns telemetry off and deletes the install ID.
func Disable(dir string) error {
	if dir == "" {
		return errNoDir
	}

	return writeState(dir, state{Enabled: false, InstallID: ""})
}

var errNoDir = errors.New("no place to save the setting: set " + envConfig)

// installID returns the saved ID, or a fresh one that lives for this process
// only when nothing can be saved (an environment-variable opt-in in a
// container without a configuration directory).
func installID(saved state) string {
	if saved.InstallID != "" {
		return saved.InstallID
	}

	id, err := newID()
	if err != nil {
		return ""
	}

	return id
}

func newID() (string, error) {
	b := make([]byte, idBytes)

	_, err := rand.Read(b)
	if err != nil {
		return "", fmt.Errorf("make install id: %w", err)
	}

	return hex.EncodeToString(b), nil
}

func statePath(dir string) string { return filepath.Join(dir, stateFile) }

func readState(dir string) state {
	if dir == "" {
		return state{Enabled: false, InstallID: ""}
	}

	b, err := os.ReadFile(statePath(dir))
	if err != nil {
		return state{Enabled: false, InstallID: ""}
	}

	var st state

	err = json.Unmarshal(b, &st)
	if err != nil {
		return state{Enabled: false, InstallID: ""}
	}

	return st
}

func writeState(dir string, st state) error {
	err := os.MkdirAll(dir, dirPerm)
	if err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode setting: %w", err)
	}

	err = os.WriteFile(statePath(dir), append(b, '\n'), filePerm)
	if err != nil {
		return fmt.Errorf("save setting: %w", err)
	}

	return nil
}

func truthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))

	return v != "" && v != "0" && v != "false" && v != "no" && v != "off"
}

// Endpoint returns where pings go: the OPENAPI_MOCKER_TELEMETRY_URL variable,
// else builtin (set at build time). Empty means no collector is configured and
// nothing is ever sent.
func Endpoint(getenv func(string) string, builtin string) string {
	if v := strings.TrimSpace(getenv(envEndpoint)); v != "" {
		return v
	}

	return builtin
}

// NewPayload builds the payload for this process. Protocols are sorted and
// without duplicates.
func NewPayload(installID, version, mode string, protocols []string) Payload {
	protocols = slices.Clone(protocols)
	slices.Sort(protocols)

	return Payload{
		Schema:    SchemaVersion,
		InstallID: installID,
		Version:   version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Mode:      mode,
		Protocols: slices.Compact(protocols),
	}
}

var errEndpoint = errors.New("the telemetry endpoint must be an https URL")

// validEndpoint accepts https, and plain http only for the local machine so
// that a collector can be tested.
func validEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errEndpoint
	}

	if u.Scheme == "https" {
		return nil
	}

	if u.Scheme == "http" && isLoopback(u.Hostname()) {
		return nil
	}

	return errEndpoint
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// Send posts the payload once. It follows no redirects and gives up after a
// few seconds.
func Send(ctx context.Context, endpoint string, p *Payload) error {
	err := validEndpoint(endpoint)
	if err != nil {
		return err
	}

	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode ping: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ping: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "openapi-mocker/"+p.Version)

	client := &http.Client{
		Transport:     nil,
		Jar:           nil,
		Timeout:       sendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send ping: %w", err)
	}

	defer resp.Body.Close()

	const maxOK = 299
	if resp.StatusCode > maxOK {
		return fmt.Errorf("%w: status %d", errStatus, resp.StatusCode)
	}

	return nil
}

var errStatus = errors.New("the collector refused the ping")

// Run sends a ping now and then every interval until ctx ends. build makes the
// payload each time, so that it reflects what is being served at that moment.
// Failures are passed to onError and never stop the process.
func Run(ctx context.Context, endpoint string, interval time.Duration, build func() Payload, onError func(error)) {
	ping := func() {
		p := build()

		err := Send(ctx, endpoint, &p)
		if err != nil && onError != nil && ctx.Err() == nil {
			onError(err)
		}
	}

	ping()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ping()
		}
	}
}
