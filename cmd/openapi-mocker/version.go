package main

import (
	"runtime/debug"
	"strings"
)

// defaultVersion is what an unversioned build reports.
const defaultVersion = "dev"

// resolveVersion picks the version a binary reports. Release builds inject it
// with -ldflags "-X main.version=1.2.3" and keep it. A binary built with
// "go install github.com/Eisenmann/openapi-mocker/cmd/openapi-mocker@v1.2.3"
// has no ldflags, but Go records the module version in the build info; the
// leading "v" is dropped so both paths print the same form. A local build
// reports "dev".
func resolveVersion(injected string, info *debug.BuildInfo) string {
	if injected != defaultVersion && injected != "" {
		return injected
	}

	if info == nil {
		return defaultVersion
	}

	v := strings.TrimPrefix(info.Main.Version, "v")
	if v == "" || v == "(devel)" {
		return defaultVersion
	}

	return v
}

// initVersion sets version from the build info when it was not injected.
func initVersion() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = nil
	}

	version = resolveVersion(version, info)
}
