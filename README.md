# comet

`comet` runs CLI commands against network devices and collects their running
configurations. It supports Juniper Junos and Arista EOS over SSH, using
[scrapligo](https://github.com/scrapli/scrapligo).

## Install

Download the archive for your platform (Linux or macOS, amd64 or arm64) from
the [releases page](https://github.com/datum-cloud/comet/releases) and put the
`comet` binary on your `PATH`. Or build from a checkout:

```sh
task build   # writes bin/comet
```

`comet` drives the system OpenSSH client, so `ssh` must be on `PATH`. Windows
is not supported; use WSL.

### Verifying a release

Each release publishes `checksums.txt`, a Sigstore signature bundle for it,
an SBOM per archive, and GitHub build provenance attestations. To check a
download, verify the checksum file's signature, then the archive against it:

```sh
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/datum-cloud/comet/\.github/workflows/release\.yaml@refs/tags/v'
sha256sum --ignore-missing -c checksums.txt
```

Or check the archive's build provenance with the GitHub CLI:

```sh
gh attestation verify comet_<version>_linux_amd64.tar.gz --repo datum-cloud/comet
```

## Authentication

Only SSH key authentication is supported. `comet` uses your ssh-agent and
default identities, or the key given with `--ssh-key` (or `ssh_key` in the
inventory). The key is passed to OpenSSH with `-i`, so a passphrase-protected
key works when it is loaded in ssh-agent; `comet` never reads it. Paths may
start with `~/`. A key or `--ssh-config` file that does not exist stops the run
before any device is contacted. Password prompts are disabled, so a device
that does not accept the key fails immediately instead of waiting.

Host keys are verified against `~/.ssh/known_hosts`, and a device with no entry
there fails to connect. Add entries before the first run, for example with
`ssh-keyscan -H 192.0.2.1 >> ~/.ssh/known_hosts`, after checking the
fingerprints. Pass `--insecure-ignore-host-key` only for lab devices. Your
`~/.ssh/config` is not read unless you pass it with `--ssh-config
~/.ssh/config`; do that to use jump hosts or per-host settings.

The username comes from `--username`, then `COMET_USERNAME`, then `$USER`.

## Usage

Collect configurations from an inventory, one `<name>.cfg` file per device
(mode `0600`):

```sh
comet config get -i inventory.yaml -o ./configs
```

Collect one device's configuration to stdout:

```sh
comet config get --host 192.0.2.2 --platform eos
```

`--platform` and `--name` apply only to `--host`; with `--inventory`, set them
in the inventory.

Limit a run to some inventory devices with `--limit`/`-l`. It takes glob
patterns matched against device names, comma-separated or repeated. A device
runs if it matches any pattern, and a pattern that matches no device is an
error:

```sh
comet config get -i inventory.yaml -o ./configs -l 'rtr*'
comet exec -i inventory.yaml -l '*.lab1,tor1a.*' -c "show version"
```

Run operational commands:

```sh
comet exec -i inventory.yaml -c "show version" -c "show interfaces terse"
```

Configurations use the platform's native hierarchical format:

| Platform | `--platform` | Configuration command |
| -------- | ------------ | --------------------- |
| Juniper Junos | `junos` | `show configuration` |
| Arista EOS | `eos` | `show running-config` |

Devices run concurrently (`--workers`, default 10). A failure on one device
does not stop the others. Each failure is printed to stderr, and `comet` exits
with status 1 if any device failed. Every device is validated before any is
contacted, so an inventory error stops the run without touching the network.

`--timeout` (default `60s`) applies to connecting and to each command. Raise it
for devices with very large configurations.

## Inventory

See [`examples/inventory.example.yaml`](examples/inventory.example.yaml). `host` and `platform`
are required; `name` defaults to `host`. `port` (1-65535), `username`,
`ssh_key` and `transport` override the matching flags for that device. Unknown
keys are rejected. With `--output-dir`, every device name must be usable as a
file name, or the run stops before any device is contacted.

## Extending

Transports implement `driver.Session` in `internal/driver` and register a
`driver.Factory` under a name from their package's `init` function; `main.go`
imports each transport for that side effect. The `--transport` flag and the
inventory `transport` field select one. NETCONF, eAPI or gNMI support is added
the same way.

To support another operating system over SSH, add an entry to the `platforms`
table in `internal/driver/ssh/ssh.go`. The entry maps the comet platform name
to a scrapligo platform definition and the platform's configuration command.

## Development

```sh
task ci   # gofmt, go vet, golangci-lint, govulncheck, go test -race, build
```

The SSH driver tests replay recorded Junos and EOS sessions through
scrapligo's file transport, so they need no devices.

## License

comet is licensed under the GNU Affero General Public License v3.0 or later.
See [LICENSE](LICENSE).
