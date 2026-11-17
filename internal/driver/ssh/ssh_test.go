// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ssh

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/scrapli/scrapligo/driver/options"
	"github.com/scrapli/scrapligo/transport"

	"go.datum.net/comet/internal/driver"
)

func target(platform string) driver.Target {
	return driver.Target{
		Name:      "dev",
		Host:      "192.0.2.1",
		Platform:  platform,
		Transport: Name,
		Username:  "admin",
		Timeout:   2 * time.Second,
	}
}

// replay opens a session whose transport replays a recorded device session
// from fixture instead of connecting over SSH.
func replay(t *testing.T, platform, fixture string) *session {
	t.Helper()

	s, err := newSession(
		target(platform),
		options.WithTransportType(transport.FileTransport),
		options.WithFileTransportFile("testdata/"+fixture),
	)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	if err := s.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	return s
}

func TestGetConfig(t *testing.T) {
	cases := []struct {
		platform, fixture string
		want              []string
		notWant           string
	}{
		{
			platform: "eos",
			fixture:  "eos-config.txt",
			want:     []string{"hostname leaf1", "interface Ethernet1", "end"},
			notWant:  "leaf1#",
		},
		{
			platform: "junos",
			fixture:  "junos-config.txt",
			want:     []string{"## Last commit", "host-name r1;"},
			notWant:  "admin@r1>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			cfg, err := replay(t, tc.platform, tc.fixture).GetConfig()
			if err != nil {
				t.Fatalf("GetConfig: %v", err)
			}

			for _, w := range tc.want {
				if !strings.Contains(cfg, w) {
					t.Errorf("config missing %q:\n%s", w, cfg)
				}
			}

			if strings.Contains(cfg, tc.notWant) {
				t.Errorf("config contains prompt %q:\n%s", tc.notWant, cfg)
			}
		})
	}
}

func TestRunCommandsDeviceError(t *testing.T) {
	out, err := replay(t, "eos", "eos-invalid.txt").RunCommands([]string{"show bogus"})
	if err == nil || !strings.Contains(err.Error(), "Invalid input") {
		t.Fatalf("RunCommands error = %v, want device error", err)
	}

	if len(out) != 0 {
		t.Fatalf("RunCommands returned output for a failed command: %+v", out)
	}
}

func TestNewRejectsInvalidTargets(t *testing.T) {
	cases := map[string]func(*driver.Target){
		"unknown platform":   func(t *driver.Target) { t.Platform = "ios" },
		"missing host":       func(t *driver.Target) { t.Host = "" },
		"missing username":   func(t *driver.Target) { t.Username = "" },
		"zero timeout":       func(t *driver.Target) { t.Timeout = 0 },
		"sub-second timeout": func(t *driver.Target) { t.Timeout = 500 * time.Millisecond },
		"host as option":     func(t *driver.Target) { t.Host = "-oProxyCommand=touch x" },
		"host with space":    func(t *driver.Target) { t.Host = "192.0.2.1 -v" },
		"username as option": func(t *driver.Target) { t.Username = "-v" },
		"username with tab":  func(t *driver.Target) { t.Username = "ops\tx" },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tgt := target("eos")
			mutate(&tgt)

			if _, err := New(tgt); err == nil {
				t.Fatal("New succeeded, want error")
			}
		})
	}
}

func TestRegistered(t *testing.T) {
	if _, err := driver.New(target("junos")); err != nil {
		t.Fatalf("driver.New via registry: %v", err)
	}
}

func TestRunCommandsRequiresOpen(t *testing.T) {
	s, err := newSession(target("eos"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.RunCommands([]string{"show version"}); err == nil {
		t.Fatal("RunCommands on unopened session succeeded")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close on unopened session: %v", err)
	}
}

// openArgs returns the argument list the system transport would pass to ssh
// for tgt. The transport's binary is a path that does not exist, so the
// arguments are built and the spawn fails without starting any process.
func openArgs(t *testing.T, tgt driver.Target) []string {
	t.Helper()

	s, err := newSession(
		tgt,
		options.WithSystemTransportOpenBin(filepath.Join(t.TempDir(), "no-ssh")),
	)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	d, err := s.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if err := d.Open(); err == nil {
		_ = d.Close()
		t.Fatal("Open with a missing ssh binary succeeded")
	}

	sys, ok := d.Transport.Impl.(*transport.System)
	if !ok {
		t.Fatalf("transport is %T, want *transport.System", d.Transport.Impl)
	}

	if len(sys.OpenArgs) == 0 {
		t.Fatal("system transport built no arguments")
	}

	return sys.OpenArgs
}

// hasArgs reports whether want appears as a contiguous run in args.
func hasArgs(args []string, want ...string) bool {
	for i := range args {
		if slices.Equal(args[i:min(i+len(want), len(args))], want) {
			return true
		}
	}

	return false
}

func writeFile(t *testing.T, path string) {
	t.Helper()

	if err := os.WriteFile(path, []byte("placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	key := filepath.Join(home, ".ssh", "id_test")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}

	writeFile(t, key)

	sshConfig := filepath.Join(t.TempDir(), "ssh_config")
	writeFile(t, sshConfig)

	cases := []struct {
		name    string
		mutate  func(*driver.Target)
		want    [][]string
		notWant [][]string
	}{
		{
			name:   "defaults",
			mutate: func(*driver.Target) {},
			want: [][]string{
				{"-o", "BatchMode=yes"},
				{"-o", "StrictHostKeyChecking=yes"},
				{"-F", "/dev/null"},
			},
			// No UserKnownHostsFile override: OpenSSH's default known_hosts applies.
			notWant: [][]string{
				{"-o", "StrictHostKeyChecking=no"},
				{"-o", "UserKnownHostsFile=/dev/null"},
				{"-i"},
			},
		},
		{
			name:   "insecure ignore host key",
			mutate: func(t *driver.Target) { t.InsecureIgnoreHostKey = true },
			want: [][]string{
				{"-o", "BatchMode=yes"},
				{"-o", "StrictHostKeyChecking=no"},
			},
			notWant: [][]string{{"-o", "StrictHostKeyChecking=yes"}},
		},
		{
			name:   "key file with tilde",
			mutate: func(t *driver.Target) { t.SSHKeyFile = "~/.ssh/id_test" },
			want: [][]string{
				{"-o", "BatchMode=yes"},
				{"-i", key},
			},
		},
		{
			name:   "ssh config file",
			mutate: func(t *driver.Target) { t.SSHConfigFile = sshConfig },
			want: [][]string{
				{"-o", "BatchMode=yes"},
				{"-F", sshConfig},
			},
			notWant: [][]string{{"-F", "/dev/null"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt := target("eos")
			tc.mutate(&tgt)

			args := openArgs(t, tgt)

			for _, w := range tc.want {
				if !hasArgs(args, w...) {
					t.Errorf("ssh args missing %q:\n%q", w, args)
				}
			}

			for _, nw := range tc.notWant {
				if hasArgs(args, nw...) {
					t.Errorf("ssh args contain %q:\n%q", nw, args)
				}
			}
		})
	}
}

func TestNewRejectsBadLocalFiles(t *testing.T) {
	dir := t.TempDir()

	paths := map[string]string{
		"missing":   filepath.Join(dir, "absent"),
		"directory": dir,
	}

	fields := map[string]func(*driver.Target, string){
		"ssh key":    func(t *driver.Target, p string) { t.SSHKeyFile = p },
		"ssh config": func(t *driver.Target, p string) { t.SSHConfigFile = p },
	}

	for field, set := range fields {
		for name, path := range paths {
			t.Run(field+"/"+name, func(t *testing.T) {
				tgt := target("eos")
				set(&tgt, path)

				_, err := New(tgt)
				if err == nil {
					t.Fatal("New succeeded, want error")
				}

				if !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), path) {
					t.Errorf("error %q does not name the %s path %q", err, field, path)
				}
			})
		}
	}
}

func TestPassphraseProtectedKey(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not available")
	}

	key := filepath.Join(t.TempDir(), "id_ed25519")

	out, err := exec.Command(
		keygen, "-q", "-t", "ed25519", "-N", "comet-test-passphrase",
		"-C", "comet-test-only", "-f", key,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}

	tgt := target("junos")
	tgt.SSHKeyFile = key

	if _, err := New(tgt); err != nil {
		t.Fatalf("New rejected a passphrase-protected key: %v", err)
	}

	if args := openArgs(t, tgt); !hasArgs(args, "-i", key) {
		t.Errorf("ssh args missing -i %s:\n%q", key, args)
	}
}
