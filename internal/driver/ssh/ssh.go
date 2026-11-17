// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ssh implements the "ssh" transport with scrapligo, driving the
// device CLI over the system OpenSSH client. Authentication is left to
// OpenSSH (ssh-agent, default identities, or a key passed with -i); comet never
// reads private keys, and password prompts are disabled.
package ssh

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/scrapli/scrapligo/driver/network"
	"github.com/scrapli/scrapligo/driver/options"
	"github.com/scrapli/scrapligo/platform"
	"github.com/scrapli/scrapligo/util"

	"go.datum.net/comet/internal/driver"
)

// Name is the transport name this package registers.
const Name = "ssh"

// platformSpec maps a comet platform to its scrapligo definition and the CLI
// command that prints the running configuration. Supporting another network
// operating system over SSH means adding an entry here.
type platformSpec struct {
	scrapli       string
	configCommand string
}

var platforms = map[string]platformSpec{
	"eos":   {scrapli: "arista_eos", configCommand: "show running-config"},
	"junos": {scrapli: "juniper_junos", configCommand: "show configuration"},
}

func init() {
	driver.Register(Name, New)
}

// Platforms returns the platform names this transport supports, sorted.
func Platforms() []string {
	names := make([]string, 0, len(platforms))
	for n := range platforms {
		names = append(names, n)
	}

	sort.Strings(names)

	return names
}

type session struct {
	target driver.Target
	spec   platformSpec
	opts   []util.Option
	d      *network.Driver
}

// New validates t and returns an unopened SSH session.
func New(t driver.Target) (driver.Session, error) {
	return newSession(t)
}

func newSession(t driver.Target, extra ...util.Option) (*session, error) {
	spec, ok := platforms[t.Platform]
	if !ok {
		return nil, fmt.Errorf(
			"platform %q is not supported by the ssh transport (supported: %s)",
			t.Platform, strings.Join(Platforms(), ", "),
		)
	}

	if err := checkArg("host", t.Host); err != nil {
		return nil, err
	}

	if err := checkArg("username", t.Username); err != nil {
		return nil, err
	}

	// OpenSSH takes ConnectTimeout and ServerAliveInterval in whole seconds,
	// and scrapligo truncates, so a shorter timeout would disable both.
	if t.Timeout < time.Second {
		return nil, fmt.Errorf("timeout must be at least 1s, got %s", t.Timeout)
	}

	// Key and agent authentication only: fail instead of waiting at a
	// password prompt.
	sshArgs := []string{"-o", "BatchMode=yes"}

	if t.SSHKeyFile != "" {
		key, err := localFile("ssh key", t.SSHKeyFile)
		if err != nil {
			return nil, err
		}

		// Passed to OpenSSH rather than scrapligo's private key option, which
		// reads and parses the key itself and so rejects passphrase-protected
		// keys even when ssh-agent holds them.
		sshArgs = append(sshArgs, "-i", key)
	}

	opts := []util.Option{
		options.WithAuthUsername(t.Username),
		options.WithTimeoutSocket(t.Timeout),
		options.WithTimeoutOps(t.Timeout),
		// Appends to the system transport's arguments; pass a single slice.
		options.WithSystemTransportOpenArgs(sshArgs),
	}

	if t.Port != 0 {
		opts = append(opts, options.WithPort(t.Port))
	}

	if t.SSHConfigFile != "" {
		cfg, err := localFile("ssh config", t.SSHConfigFile)
		if err != nil {
			return nil, err
		}

		opts = append(opts, options.WithSSHConfigFile(cfg))
	}

	if t.InsecureIgnoreHostKey {
		opts = append(opts, options.WithAuthNoStrictKey())
	}

	return &session{target: t, spec: spec, opts: append(opts, extra...)}, nil
}

// checkArg rejects values that OpenSSH would misread when passed on its
// command line: empty values, a leading "-" (parsed as an option, since the
// host is passed before the options), and whitespace or control characters.
func checkArg(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is required", field)
	}

	if strings.HasPrefix(v, "-") {
		return fmt.Errorf("%s %q must not start with \"-\"", field, v)
	}

	if strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("%s %q must not contain whitespace or control characters", field, v)
	}

	return nil
}

// localFile expands a leading "~" to the user's home directory, makes the path
// absolute, and checks that it names a regular file. It does not read the
// file. kind names the file in errors.
func localFile(kind, p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding %s path %q: %w", kind, p, err)
		}

		p = filepath.Join(home, p[1:])
	}

	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", kind, p, err)
	}

	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", kind, abs, err)
	}

	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s %q is not a regular file", kind, abs)
	}

	return abs, nil
}

func (s *session) build() (*network.Driver, error) {
	p, err := platform.NewPlatform(s.spec.scrapli, s.target.Host, s.opts...)
	if err != nil {
		return nil, fmt.Errorf("building %s driver: %w", s.target.Platform, err)
	}

	d, err := p.GetNetworkDriver()
	if err != nil {
		return nil, fmt.Errorf("building %s driver: %w", s.target.Platform, err)
	}

	return d, nil
}

func (s *session) Open() error {
	d, err := s.build()
	if err != nil {
		return err
	}

	if err := d.Open(); err != nil {
		return fmt.Errorf("connecting to %s: %w", s.target.Host, err)
	}

	s.d = d

	return nil
}

func (s *session) Close() error {
	if s.d == nil {
		return nil
	}

	err := s.d.Close()
	s.d = nil

	return err
}

func (s *session) GetConfig() (string, error) {
	out, err := s.RunCommands([]string{s.spec.configCommand})
	if err != nil {
		return "", err
	}

	return out[0].Result, nil
}

func (s *session) RunCommands(cmds []string) ([]driver.Output, error) {
	if s.d == nil {
		return nil, fmt.Errorf("session is not open")
	}

	out := make([]driver.Output, 0, len(cmds))

	for _, c := range cmds {
		r, err := s.d.SendCommand(c)
		if err != nil {
			return out, fmt.Errorf("running %q: %w", c, err)
		}

		if r.Failed != nil {
			return out, fmt.Errorf("running %q: %w", c, r.Failed)
		}

		out = append(out, driver.Output{Command: c, Result: r.Result})
	}

	return out, nil
}
