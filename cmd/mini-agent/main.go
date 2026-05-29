// Command mini-agent is the process entry point.
//
// Keep this file minimal: it only forwards build-time ldflags into the
// cobra tree (internal/cli/cmd) and exits with whatever code Execute
// returns. All command behavior lives in internal/cli/cmd so it can be
// unit-tested without spawning a subprocess.
//
// Build-time injection (see Makefile):
//
//	go build -ldflags "
//	  -X main.version=v0.1.0
//	  -X main.commit=abcdef
//	  -X main.buildTime=2026-05-24T12:00:00Z
//	"
package main

import (
	"os"

	clicmd "github.com/HBulgat/mini-agent/internal/cli/cmd"
)

// Populated via -ldflags at build time. Defaults are fine for `go run`.
var (
	version   = "0.0.0-dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	clicmd.SetBuildInfo(version, commit, buildTime)
	os.Exit(clicmd.Execute())
}
