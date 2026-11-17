# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

`comet` is a Go CLI that runs operational commands against network devices and collects their running configurations (Juniper Junos and Arista EOS over SSH, via scrapligo). `README.md` is the user-facing reference for flags, inventory format and authentication.

## Commands

```sh
task ci      # lint, vuln, test, build (run before committing)
task lint    # gofmt -l, go vet, golangci-lint (pinned in Taskfile.yml, configured in .golangci.yaml)
task vuln    # govulncheck (pinned in Taskfile.yml)
task test    # go test -race ./...
task build   # writes bin/comet
task notice  # regenerate NOTICE from the module graph (go-licenses, template .notice.tmpl)

go test -race ./internal/cli -run TestLimit   # a single test
```

Tests need no devices or network access. Running the binary against real devices needs the system `ssh` on `PATH`.

The module is `go.datum.net/comet` and `go.mod` pins `toolchain go1.27.4`. `task lint` and `task vuln` run their tools with `go run` at the versions in `Taskfile.yml`, so they build with the module's toolchain; bump a tool by editing its version var there. gosec is off for `_test.go` files; a production `//nolint:gosec` needs the rule ID and a reason.

## CI and releases

- `.github/workflows/ci.yaml` runs lint, govulncheck, tests, a build for linux and darwin on amd64 and arm64, a NOTICE drift check (`task notice` must leave `NOTICE` unchanged; rerun it after any dependency change and commit the result) and `goreleaser check`.
- `.github/workflows/update-notice.yaml` regenerates `NOTICE` and commits it to non-`main` branches whose push changes `go.mod` or `go.sum` (for example Renovate's). That commit is made with `GITHUB_TOKEN`, so it does not trigger CI itself; push again or re-run CI on the branch.
- `.github/workflows/required-ci.yaml` reports `ci / Build and test`, the status check the datum-cloud organization ruleset requires on `main`. Don't rename its `ci` job.
- `.github/workflows/release.yaml` runs on `vX.Y.Z` tags that point at a commit on `main`. It reruns CI, then GoReleaser (`.goreleaser.yaml`) publishes lowercase-named archives, `checksums.txt` signed with cosign, per-archive SBOMs and build provenance attestations.
- Windows is not a supported platform: `github.com/creack/pty` has no Windows support, so the SSH transport cannot open a session there. It compiles, but keep it out of the build matrix and release config.
- Release builds set `main.version` at link time; `--version` falls back to the module version for `go install` builds and `dev` otherwise.

## Licensing

comet is AGPL-3.0-or-later. Every `.go` file starts with this header, followed by a blank line so it is not taken as the package doc comment:

```go
// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later
```

## Architecture

Three layers, wired together in `main.go` (which also resolves the version passed to `cli.Execute`):

- **`internal/driver`**: the transport-neutral contract. `Target` describes one device and how to reach it; `Session` (`Open`, `Close`, `GetConfig`, `RunCommands`) is the connection. Transports register a `Factory` by name in a global registry from their package `init`, and `main.go` blank-imports each transport package for that side effect. A `Factory` must validate the target and do **no network I/O**; this is what lets the CLI reject a bad inventory before contacting any device.
- **`internal/driver/ssh`**: the only transport. The `platforms` map in `ssh.go` maps a comet platform name to a scrapligo platform and its config command; adding an OS over SSH means adding an entry there. Sessions force `BatchMode=yes` (key/agent auth only, never a password prompt). The key file goes to OpenSSH as `-i` (never through scrapligo's key option, which reads and parses the key and rejects passphrase-protected ones); `newSession` only expands `~` and stats the key and `--ssh-config` files. `TestOpenArgs` pins the argv passed to `ssh`, including strict host key checking by default.
- **`internal/inventory`**: YAML inventory loading with `KnownFields(true)`, so unknown keys are errors.
- **`internal/cli`**: cobra commands (`config get`, `exec`). `globalOptions.targets()` resolves `--inventory` or `--host` into `[]driver.Target`, applying flags as defaults for fields a device leaves unset, then filters with `--limit` (`path.Match` globs; a pattern matching nothing is an error). Commands call `targets()` once, then `newPlan` builds every session (the validation pass; `config get -o` also checks every name as a file name before it, and creates the output directory only after it), and the generic `run[T]` runs them with a `--workers` semaphore and returns results in target order. Per-device failures don't stop other devices; `reportFailures` prints them and turns any failure into exit status 1. `writeConfig` writes through a temp file and rename, so files always end up 0600 and an interrupted run never leaves a truncated config.

## Testing patterns

- CLI tests (`internal/cli/cli_test.go`) inject a fake `newSession` through `globalOptions` and drive `newRootCommand` with captured stdout/stderr. In the fake, a host starting with `fail` fails to `Open`, and platform `bad` fails validation.
- SSH driver tests replay recorded device sessions from `internal/driver/ssh/testdata/*.txt` through scrapligo's `FileTransport` (passed as extra options to the unexported `newSession`). New platform support should come with a recorded fixture in the same format.

## Local files

`inventory.yaml`, `configs/` and `bin/` are gitignored and may contain real device data; never commit them. Use `examples/inventory.example.yaml` and documentation-range addresses (`192.0.2.0/24`) in fixtures and docs.
