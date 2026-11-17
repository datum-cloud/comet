// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command comet runs CLI commands against network devices and collects their
// configurations.
package main

import (
	"os"
	"runtime/debug"

	"go.datum.net/comet/internal/cli"

	// Registered transports.
	_ "go.datum.net/comet/internal/driver/ssh"
)

// version is set at link time by release builds (-X main.version=...).
var version = ""

func main() {
	os.Exit(cli.Execute(buildVersion()))
}

// buildVersion returns the link-time version, or the module version recorded
// by "go install go.datum.net/comet@<version>", or "dev" for local builds.
func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}

	return "dev"
}
