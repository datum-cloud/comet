// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cli implements the comet command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"go.datum.net/comet/internal/driver"
	"go.datum.net/comet/internal/inventory"
)

// globalOptions are the flags shared by every subcommand. Connection flags act
// as defaults for inventory devices that leave the field unset.
type globalOptions struct {
	inventory    string
	host         string
	platform     string
	name         string
	port         int
	transport    string
	username     string
	sshKey       string
	sshConfig    string
	insecureHost bool
	timeout      time.Duration
	workers      int
	limit        []string
	stdout       io.Writer
	stderr       io.Writer
	newSession   func(driver.Target) (driver.Session, error)
}

// NewRootCommand returns the comet root command.
func NewRootCommand() *cobra.Command {
	return newRootCommand(&globalOptions{
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		newSession: driver.New,
	})
}

func newRootCommand(o *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "comet",
		Short:         "Run commands against network devices and collect their configurations",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	f := cmd.PersistentFlags()
	f.StringVarP(&o.inventory, "inventory", "i", "", "path to a YAML device inventory")
	f.StringVar(&o.host, "host", "", "single device address (instead of --inventory)")
	f.StringVar(&o.platform, "platform", "", "platform for --host: junos or eos")
	f.StringVar(&o.name, "name", "", "device name for --host (default: the host)")
	f.IntVar(&o.port, "port", 0, "port (default: the transport default)")
	f.StringVar(&o.transport, "transport", "ssh", "transport to use")
	f.StringVarP(&o.username, "username", "u", "", "login username (default: $COMET_USERNAME, then $USER)")
	f.StringVar(&o.sshKey, "ssh-key", "", "SSH private key file (default: ssh-agent and default identities)")
	f.StringVar(&o.sshConfig, "ssh-config", "", "ssh_config file to use, for example for jump hosts")
	f.BoolVar(&o.insecureHost, "insecure-ignore-host-key", false, "skip SSH host key verification")
	f.DurationVar(&o.timeout, "timeout", 60*time.Second, "timeout for connecting and for each command")
	f.IntVarP(&o.workers, "workers", "w", 10, "number of devices to process concurrently")
	f.StringSliceVarP(&o.limit, "limit", "l", nil,
		"only run devices whose name matches one of these glob patterns (comma-separated or repeated)")

	cmd.AddCommand(newConfigCommand(o), newExecCommand(o))

	return cmd
}

// Execute runs the root command, reporting version for --version, and returns
// the process exit code.
func Execute(version string) int {
	cmd := NewRootCommand()
	cmd.Version = version
	if err := cmd.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)

		return 1
	}

	return 0
}

// targets resolves the device list from --inventory or --host and applies the
// connection flags as defaults. The username falls back to $COMET_USERNAME,
// then $USER, when neither the device nor --username sets it.
func (o *globalOptions) targets() ([]driver.Target, error) {
	var devs []inventory.Device

	switch {
	case o.inventory != "" && o.host != "":
		return nil, errors.New("use either --inventory or --host, not both")
	case o.inventory != "":
		if o.name != "" || o.platform != "" {
			return nil, errors.New("--name and --platform apply only to --host; set them in the inventory instead")
		}

		inv, err := inventory.Load(o.inventory)
		if err != nil {
			return nil, err
		}

		devs = inv.Devices
	case o.host != "":
		if o.platform == "" {
			return nil, errors.New("--platform is required with --host")
		}

		if strings.IndexFunc(o.name, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("--name %q contains control characters", o.name)
		}

		devs = []inventory.Device{{
			Name:     firstNonEmpty(o.name, o.host),
			Host:     o.host,
			Platform: o.platform,
		}}
	default:
		return nil, errors.New("specify devices with --inventory or --host")
	}

	if o.workers < 1 {
		return nil, errors.New("--workers must be at least 1")
	}

	if o.port < 0 || o.port > 65535 {
		return nil, fmt.Errorf("--port %d is out of range 0-65535", o.port)
	}

	username := firstNonEmpty(o.username, os.Getenv("COMET_USERNAME"), os.Getenv("USER"))

	seen := map[string]bool{}
	targets := make([]driver.Target, 0, len(devs))

	for _, d := range devs {
		if seen[d.Name] {
			return nil, fmt.Errorf("duplicate device name %q", d.Name)
		}

		seen[d.Name] = true

		port := d.Port
		if port == 0 {
			port = o.port
		}

		targets = append(targets, driver.Target{
			Name:                  d.Name,
			Host:                  d.Host,
			Port:                  port,
			Platform:              d.Platform,
			Transport:             firstNonEmpty(d.Transport, o.transport),
			Username:              firstNonEmpty(d.Username, username),
			SSHKeyFile:            firstNonEmpty(d.SSHKey, o.sshKey),
			SSHConfigFile:         o.sshConfig,
			InsecureIgnoreHostKey: o.insecureHost,
			Timeout:               o.timeout,
		})
	}

	return limitTargets(targets, o.limit)
}

// limitTargets keeps the targets whose name matches any of patterns, using
// path.Match glob syntax. No patterns keeps every target. A pattern that
// matches no device is an error, since it usually indicates a typo.
func limitTargets(targets []driver.Target, patterns []string) ([]driver.Target, error) {
	if len(patterns) == 0 {
		return targets, nil
	}

	matched := make([]bool, len(patterns))
	kept := make([]driver.Target, 0, len(targets))

	for _, t := range targets {
		keep := false

		for i, p := range patterns {
			ok, err := path.Match(p, t.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid --limit pattern %q: %w", p, err)
			}

			if ok {
				matched[i] = true
				keep = true
			}
		}

		if keep {
			kept = append(kept, t)
		}
	}

	for i, p := range patterns {
		if !matched[i] {
			return nil, fmt.Errorf("--limit pattern %q matches no device", p)
		}
	}

	return kept, nil
}

// result is the outcome of running one device's work.
type result[T any] struct {
	target driver.Target
	value  T
	err    error
}

// plan is a validated set of devices, each with a session that has not been
// opened yet.
type plan struct {
	targets  []driver.Target
	sessions []driver.Session
}

// newPlan builds a session for every target, which validates each one
// without network I/O. Any invalid target fails the whole plan, so no device
// is contacted.
func (o *globalOptions) newPlan(targets []driver.Target) (*plan, error) {
	p := &plan{targets: targets, sessions: make([]driver.Session, len(targets))}

	for i, t := range targets {
		s, err := o.newSession(t)
		if err != nil {
			return nil, fmt.Errorf("device %q: %w", t.Name, err)
		}

		p.sessions[i] = s
	}

	return p, nil
}

// run runs fn against each device in p with at most o.workers in flight.
// Results are returned in target order.
func run[T any](o *globalOptions, p *plan, fn func(driver.Session) (T, error)) []result[T] {
	targets, sessions := p.targets, p.sessions
	results := make([]result[T], len(targets))
	sem := make(chan struct{}, o.workers)

	var wg sync.WaitGroup

	for i := range targets {
		wg.Add(1)

		sem <- struct{}{}

		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			results[i] = result[T]{target: targets[i]}
			results[i].value, results[i].err = withSession(sessions[i], fn)
		}()
	}

	wg.Wait()

	return results
}

func withSession[T any](s driver.Session, fn func(driver.Session) (T, error)) (T, error) {
	var zero T

	if err := s.Open(); err != nil {
		_ = s.Close()

		return zero, err
	}

	v, err := fn(s)
	if cerr := s.Close(); err == nil && cerr != nil {
		return v, fmt.Errorf("closing session: %w", cerr)
	}

	return v, err
}

// reportFailures prints each device error to stderr and returns an error
// summarising how many devices failed.
func reportFailures[T any](o *globalOptions, results []result[T]) error {
	failed := 0

	for _, r := range results {
		if r.err != nil {
			failed++

			_, _ = fmt.Fprintf(o.stderr, "%s: error: %v\n", r.target.Name, r.err)
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d devices failed", failed, len(results))
	}

	return nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}

	return ""
}
