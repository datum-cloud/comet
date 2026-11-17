// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package driver defines the transport-neutral contract comet uses to talk to
// network devices, and a registry that maps transport names (ssh, netconf,
// eapi, gnmi, ...) to the implementations that provide them.
//
// A transport registers a Factory from its package's init function; callers
// import the transport package for its side effect and then use New.
package driver

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Target describes one device and how to reach it.
type Target struct {
	// Name identifies the device in output and output file names.
	Name string
	// Host is the address or DNS name to connect to.
	Host string
	// Port is the transport port; zero selects the transport default.
	Port int
	// Platform is the network operating system, for example "junos" or "eos".
	Platform string
	// Transport selects the registered transport, for example "ssh".
	Transport string
	// Username is the login user.
	Username string
	// SSHKeyFile is an optional private key path. When empty, SSH transports
	// rely on the user's ssh-agent and default identities.
	SSHKeyFile string
	// SSHConfigFile is an optional ssh_config(5) file, for example to use a
	// jump host.
	SSHConfigFile string
	// InsecureIgnoreHostKey disables host key verification.
	InsecureIgnoreHostKey bool
	// Timeout bounds connection setup and each individual operation.
	Timeout time.Duration
}

// Output is the result of one command.
type Output struct {
	Command string
	Result  string
}

// Session is an open or openable connection to one device.
type Session interface {
	// Open connects and authenticates.
	Open() error
	// Close releases the connection. It is safe to call after a failed Open.
	Close() error
	// GetConfig returns the device's running configuration in the platform's
	// native text format.
	GetConfig() (string, error)
	// RunCommands runs operational commands in order and stops at the first
	// failure.
	RunCommands(cmds []string) ([]Output, error)
}

// Factory validates t and returns an unopened Session. It must not perform
// network I/O, so that every target can be validated before any connects.
type Factory func(t Target) (Session, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a transport available under name. It panics on a duplicate
// name, which indicates a programming error.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()

	if _, ok := factories[name]; ok {
		panic(fmt.Sprintf("driver: transport %q registered twice", name))
	}

	factories[name] = f
}

// Transports returns the registered transport names, sorted.
func Transports() []string {
	mu.RLock()
	defer mu.RUnlock()

	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}

	sort.Strings(names)

	return names
}

// New returns an unopened Session for t using the transport t names.
func New(t Target) (Session, error) {
	mu.RLock()
	f, ok := factories[t.Transport]
	mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf(
			"unsupported transport %q (supported: %s)",
			t.Transport, strings.Join(Transports(), ", "),
		)
	}

	return f(t)
}
