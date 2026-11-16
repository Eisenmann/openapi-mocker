package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"slices"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/telemetry"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// telemetryEndpoint is where usage pings go. It is set at build time:
// -ldflags "-X main.telemetryEndpoint=https://...". A build without it sends
// nothing, whatever the user chose.
var telemetryEndpoint = ""

const telemetryUsage = `Usage: openapi-mocker telemetry status|enable|disable|show

OpenAPI Mocker can send an anonymous usage ping. It is off until you turn it
on, and it helps decide what gets built next.

  status    say whether it is on, and why
  enable    turn it on
  disable   turn it off and delete the random install id
  show      print exactly what a ping contains

DO_NOT_TRACK=1 always turns it off. OPENAPI_MOCKER_TELEMETRY=1 or =0 turns it
on or off for one environment, for example a container. What is sent, and what
is not, is described in docs/privacy.md.
`

// Contract kinds reported by the ping.
const (
	protocolOpenAPI = "openapi"
	protocolGraphQL = "graphql"
	protocolMCP     = "mcp"
	protocolGRPC    = "grpc"
)

// runTelemetry is the telemetry command; it returns the process exit code.
func runTelemetry(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprint(stderr, telemetryUsage)
		return exitUsage
	}

	dir := telemetry.Dir(os.Getenv, "")

	switch args[0] {
	case "status":
		return telemetryStatus(stdout, dir)
	case "enable":
		return telemetryEnable(stdout, stderr, dir)
	case "disable":
		return telemetryDisable(stdout, stderr, dir)
	case "show":
		return telemetryShow(stdout, stderr, dir)
	default:
		_, _ = fmt.Fprintf(stderr, "telemetry: unknown command %q\n\n%s", args[0], telemetryUsage)
		return exitUsage
	}
}

func telemetryStatus(stdout io.Writer, dir string) int {
	s := telemetry.Load(os.Getenv, dir)

	state := "off"
	if s.Enabled {
		state = "on"
	}

	_, _ = fmt.Fprintf(stdout, "Usage ping: %s (%s)\n", state, s.Source)

	endpoint := telemetry.Endpoint(os.Getenv, telemetryEndpoint)
	if endpoint == "" {
		_, _ = fmt.Fprintln(stdout, "Collector: none configured in this build, so nothing is sent")
	} else {
		_, _ = fmt.Fprintf(stdout, "Collector: %s\n", endpoint)
	}

	if s.InstallID != "" {
		_, _ = fmt.Fprintf(stdout, "Install id: %s\n", s.InstallID)
	}

	return exitOK
}

func telemetryEnable(stdout, stderr io.Writer, dir string) int {
	s, err := telemetry.Enable(dir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "telemetry: %v\n", err)
		return exitIOFailure
	}

	_, _ = fmt.Fprintln(stdout, "Usage ping is on. Run \"openapi-mocker telemetry show\" to see what it sends.")
	_, _ = fmt.Fprintf(stdout, "Install id: %s\n", s.InstallID)

	if telemetry.Endpoint(os.Getenv, telemetryEndpoint) == "" {
		_, _ = fmt.Fprintln(stdout, "This build has no collector configured, so nothing is sent.")
	}

	return exitOK
}

func telemetryDisable(stdout, stderr io.Writer, dir string) int {
	err := telemetry.Disable(dir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "telemetry: %v\n", err)
		return exitIOFailure
	}

	_, _ = fmt.Fprintln(stdout, "Usage ping is off and the install id is deleted.")

	return exitOK
}

// telemetryShow prints a ping as it would be sent, without sending it.
func telemetryShow(stdout, stderr io.Writer, dir string) int {
	s := telemetry.Load(os.Getenv, dir)

	id := s.InstallID
	if id == "" {
		id = "(made when you turn the ping on)"
	}

	p := telemetry.NewPayload(id, version, telemetry.ModeServe, []string{protocolOpenAPI})

	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "telemetry: %v\n", err)
		return exitIOFailure
	}

	_, _ = fmt.Fprintf(stdout, "%s\n", b)
	_, _ = fmt.Fprintln(stdout, "\nThis is the whole ping. \"protocols\" lists the kinds of contract being served.")

	return exitOK
}

// startTelemetry begins the usage ping in the background when the user turned
// it on and the build has a collector. protocols is read at every ping.
func startTelemetry(ctx context.Context, mode, fallbackDir string, protocols func() []string) {
	dir := telemetry.Dir(os.Getenv, fallbackDir)
	s := telemetry.Load(os.Getenv, dir)
	endpoint := telemetry.Endpoint(os.Getenv, telemetryEndpoint)

	if !s.Enabled || endpoint == "" || s.InstallID == "" {
		return
	}

	build := func() telemetry.Payload {
		return telemetry.NewPayload(s.InstallID, version, mode, protocols())
	}

	go telemetry.Run(ctx, endpoint, telemetry.Interval, build, func(err error) {
		log.Printf("usage ping: %v", err)
	})
}

// telemetryHint tells a person at a terminal that the ping exists, once per
// start while they have not chosen. It says nothing in CI or when output is
// piped, and nothing when this build has no collector.
func telemetryHint(w io.Writer, fallbackDir string) {
	if telemetry.Endpoint(os.Getenv, telemetryEndpoint) == "" {
		return
	}

	dir := telemetry.Dir(os.Getenv, fallbackDir)
	if telemetry.Load(os.Getenv, dir).Decided {
		return
	}

	f, ok := w.(*os.File)
	if !ok {
		return
	}

	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}

	_, _ = fmt.Fprintln(w,
		"Anonymous usage statistics are off. To help decide what gets built: openapi-mocker telemetry enable")
}

// protocolOf names the kind of a contract.
func protocolOf(raw string) string {
	switch {
	case usecase.IsMCP(raw):
		return protocolMCP
	case usecase.IsGraphQL(raw):
		return protocolGraphQL
	case usecase.IsProto(raw):
		return protocolGRPC
	default:
		return protocolOpenAPI
	}
}

// storeProtocols lists the kinds of the active contracts of all projects.
func storeProtocols(store *jsonstore.Store) []string {
	var kinds []string

	for _, p := range store.List() {
		c, err := store.GetActive(p.ID)
		if err != nil || c == nil {
			continue
		}

		kind := protocolOf(c.Raw)
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}

	return kinds
}
