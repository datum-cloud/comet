// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.datum.net/comet/internal/driver"
)

// fakeSession returns canned output. A target whose host starts with "fail"
// fails to connect. Open records the call and how many sessions are open at
// once, and waits for the harness's per-device delay.
type fakeSession struct {
	t driver.Target
	h *harness
}

func (f *fakeSession) Open() error {
	f.h.opens.Add(1)

	n := f.h.active.Add(1)
	for {
		peak := f.h.peak.Load()
		if n <= peak || f.h.peak.CompareAndSwap(peak, n) {
			break
		}
	}

	time.Sleep(f.h.delay[f.t.Name])

	if strings.HasPrefix(f.t.Host, "fail") {
		return errors.New("connection refused")
	}

	return nil
}

func (f *fakeSession) Close() error {
	f.h.active.Add(-1)

	return nil
}

func (f *fakeSession) GetConfig() (string, error) { return "hostname " + f.t.Name, nil }

func (f *fakeSession) RunCommands(cmds []string) ([]driver.Output, error) {
	out := make([]driver.Output, len(cmds))
	for i, c := range cmds {
		out[i] = driver.Output{Command: c, Result: f.t.Name + " " + c}
	}

	return out, nil
}

type harness struct {
	stdout, stderr bytes.Buffer

	// env, when set, replaces the default environment of COMET_USERNAME=ops.
	env map[string]string
	// delay holds how long Open takes, by device name.
	delay map[string]time.Duration

	opens, active, peak atomic.Int32

	mu      sync.Mutex
	targets []driver.Target
}

// execute runs comet with args against fake sessions. The "bad" platform is
// rejected at validation, as a real transport rejects unknown platforms.
func (h *harness) execute(t *testing.T, args ...string) error {
	t.Helper()

	env := h.env
	if env == nil {
		env = map[string]string{"COMET_USERNAME": "ops"}
	}

	for k, v := range env {
		t.Setenv(k, v)
	}

	o := &globalOptions{
		stdout: &h.stdout,
		stderr: &h.stderr,
		newSession: func(tgt driver.Target) (driver.Session, error) {
			h.mu.Lock()
			defer h.mu.Unlock()

			if tgt.Platform == "bad" {
				return nil, errors.New("unsupported platform")
			}

			h.targets = append(h.targets, tgt)

			return &fakeSession{t: tgt, h: h}, nil
		},
	}

	cmd := newRootCommand(o)
	cmd.SetArgs(args)

	return cmd.Execute()
}

func writeInventory(t *testing.T, doc string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "inventory.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

const twoDevices = `
devices:
  - {name: core1, host: 192.0.2.1, platform: junos, username: admin, port: 830}
  - {name: leaf1, host: 192.0.2.2, platform: eos}
`

func TestConfigGetWritesFilePerDevice(t *testing.T) {
	var h harness

	out := filepath.Join(t.TempDir(), "configs")

	if err := h.execute(t, "config", "get", "-i", writeInventory(t, twoDevices), "-o", out, "--ssh-key", "/k"); err != nil {
		t.Fatalf("config get: %v\nstderr: %s", err, h.stderr.String())
	}

	for _, name := range []string{"core1", "leaf1"} {
		p := filepath.Join(out, name+".cfg")

		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}

		if string(b) != "hostname "+name+"\n" {
			t.Errorf("%s = %q", p, b)
		}

		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, fi.Mode().Perm())
		}
	}

	// Inventory values override flags; flags fill unset fields.
	byName := map[string]driver.Target{}
	for _, tgt := range h.targets {
		byName[tgt.Name] = tgt
	}

	if c := byName["core1"]; c.Username != "admin" || c.Port != 830 || c.Transport != "ssh" || c.SSHKeyFile != "/k" {
		t.Errorf("core1 target = %+v", c)
	}

	if l := byName["leaf1"]; l.Username != "ops" || l.Port != 0 {
		t.Errorf("leaf1 target = %+v", l)
	}
}

func TestConfigGetSingleHostToStdout(t *testing.T) {
	var h harness

	if err := h.execute(t, "config", "get", "--host", "192.0.2.9", "--platform", "eos"); err != nil {
		t.Fatalf("config get: %v", err)
	}

	if got := h.stdout.String(); got != "hostname 192.0.2.9\n" {
		t.Errorf("stdout = %q", got)
	}
}

func TestConfigGetPartialFailure(t *testing.T) {
	var h harness

	inv := writeInventory(t, `
devices:
  - {name: ok, host: 192.0.2.1, platform: eos}
  - {name: down, host: fail.example, platform: eos}
`)
	out := t.TempDir()

	err := h.execute(t, "config", "get", "-i", inv, "-o", out)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 devices failed") {
		t.Fatalf("error = %v, want partial failure", err)
	}

	if _, err := os.Stat(filepath.Join(out, "ok.cfg")); err != nil {
		t.Errorf("healthy device config not written: %v", err)
	}

	if !strings.Contains(h.stderr.String(), "down: error: connection refused") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestValidationErrors(t *testing.T) {
	inv := writeInventory(t, twoDevices)

	cases := map[string]struct {
		args []string
		want string
	}{
		"no devices":         {[]string{"config", "get"}, "--inventory or --host"},
		"both sources":       {[]string{"config", "get", "-i", inv, "--host", "x"}, "not both"},
		"host needs platfrm": {[]string{"config", "get", "--host", "x"}, "--platform is required"},
		"multi needs outdir": {[]string{"config", "get", "-i", inv}, "--output-dir is required"},
		"exec needs command": {[]string{"exec", "--host", "x", "--platform", "eos"}, "--command"},
		"bad workers":        {[]string{"exec", "-i", inv, "-c", "x", "-w", "0"}, "--workers"},
		"inventory and name": {[]string{"exec", "-i", inv, "-c", "x", "--name", "r1"}, "apply only to --host"},
		"inventory platform": {[]string{"exec", "-i", inv, "-c", "x", "--platform", "eos"}, "apply only to --host"},
		"port too high":      {[]string{"exec", "-i", inv, "-c", "x", "--port", "65536"}, "--port 65536 is out of range"},
		"negative port":      {[]string{"exec", "--host", "x", "--platform", "eos", "-c", "x", "--port", "-1"}, "out of range"},
		"duplicate names": {[]string{"exec", "-c", "x", "-i", writeInventory(t, `
devices:
  - {name: a, host: 192.0.2.1, platform: eos}
  - {name: a, host: 192.0.2.2, platform: eos}
`)}, "duplicate device name"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var h harness

			err := h.execute(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// An invalid device must stop the run before any device is contacted.
func TestInvalidTargetStopsBeforeConnecting(t *testing.T) {
	var h harness

	inv := writeInventory(t, `
devices:
  - {name: good, host: 192.0.2.1, platform: eos}
  - {name: broken, host: 192.0.2.2, platform: bad}
`)

	err := h.execute(t, "config", "get", "-i", inv, "-o", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `device "broken"`) {
		t.Fatalf("error = %v", err)
	}

	if h.stdout.Len() != 0 || strings.Contains(h.stderr.String(), "wrote") {
		t.Errorf("devices were contacted: stdout=%q stderr=%q", h.stdout.String(), h.stderr.String())
	}
}

func TestExecOutput(t *testing.T) {
	var h harness

	if err := h.execute(t, "exec", "-i", writeInventory(t, twoDevices), "-c", "show version", "-c", "show clock"); err != nil {
		t.Fatalf("exec: %v", err)
	}

	want := `==> core1 <==
--- show version
core1 show version
--- show clock
core1 show clock
==> leaf1 <==
--- show version
leaf1 show version
--- show clock
leaf1 show clock
`
	if got := h.stdout.String(); got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteConfigRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../x", `a\b`} {
		if _, err := writeConfig(t.TempDir(), name, "x"); err == nil {
			t.Errorf("writeConfig(%q) succeeded", name)
		}
	}
}

func TestLimit(t *testing.T) {
	inv := writeInventory(t, `
devices:
  - {name: rtr1.lab1, host: 192.0.2.1, platform: junos}
  - {name: rtr1.lab2, host: 192.0.2.2, platform: junos}
  - {name: tor1a.lab1, host: 192.0.2.3, platform: eos}
`)

	cases := map[string]struct {
		limit []string
		want  string
	}{
		"glob":             {[]string{"-l", "rtr*"}, "==> rtr1.lab1 <==\n--- x\nrtr1.lab1 x\n==> rtr1.lab2 <==\n--- x\nrtr1.lab2 x\n"},
		"comma list":       {[]string{"-l", "*.lab2,tor1a.*"}, "==> rtr1.lab2 <==\n--- x\nrtr1.lab2 x\n==> tor1a.lab1 <==\n--- x\ntor1a.lab1 x\n"},
		"repeated, single": {[]string{"--limit", "nope*", "--limit", "*"}, ""},
		"exact name":       {[]string{"-l", "tor1a.lab1"}, "--- x\ntor1a.lab1 x\n"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var h harness

			err := h.execute(t, append([]string{"exec", "-i", inv, "-c", "x"}, tc.limit...)...)
			if tc.want == "" {
				if err == nil || !strings.Contains(err.Error(), `"nope*" matches no device`) {
					t.Fatalf("error = %v, want no-match error", err)
				}

				if len(h.targets) != 0 {
					t.Fatalf("sessions created despite error: %v", h.targets)
				}

				return
			}

			if err != nil {
				t.Fatalf("exec: %v", err)
			}

			if got := h.stdout.String(); got != tc.want {
				t.Errorf("stdout:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestLimitInvalidPattern(t *testing.T) {
	var h harness

	err := h.execute(t, "exec", "-i", writeInventory(t, twoDevices), "-c", "x", "-l", "[")
	if err == nil || !strings.Contains(err.Error(), "invalid --limit pattern") {
		t.Fatalf("error = %v", err)
	}
}

// A single device left by --limit may print its configuration to stdout.
func TestLimitToOneDeviceAllowsStdout(t *testing.T) {
	var h harness

	if err := h.execute(t, "config", "get", "-i", writeInventory(t, twoDevices), "-l", "leaf1"); err != nil {
		t.Fatalf("config get: %v", err)
	}

	if got := h.stdout.String(); got != "hostname leaf1\n" {
		t.Errorf("stdout = %q", got)
	}
}

// With --output-dir, an unusable device name stops the run before any session
// is built, and the output directory is not created.
func TestConfigGetUnsafeNameStopsBeforeConnecting(t *testing.T) {
	var h harness

	inv := writeInventory(t, `
devices:
  - {name: good, host: 192.0.2.1, platform: eos}
  - {name: ../x, host: 192.0.2.2, platform: eos}
`)
	out := filepath.Join(t.TempDir(), "configs")

	err := h.execute(t, "config", "get", "-i", inv, "-o", out)
	if err == nil || !strings.Contains(err.Error(), "cannot be used as a file name") {
		t.Fatalf("error = %v", err)
	}

	if len(h.targets) != 0 {
		t.Errorf("sessions created despite invalid name: %v", h.targets)
	}

	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("output directory created: %v", err)
	}
}

// Validation failures leave the output directory uncreated.
func TestConfigGetValidationDoesNotCreateOutputDir(t *testing.T) {
	cases := map[string]string{
		"invalid inventory": "devices: []",
		"invalid platform":  "devices: [{name: r1, host: 192.0.2.1, platform: bad}]",
	}

	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			var h harness

			out := filepath.Join(t.TempDir(), "configs")

			if err := h.execute(t, "config", "get", "-i", writeInventory(t, doc), "-o", out); err == nil {
				t.Fatal("config get succeeded")
			}

			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("output directory created: %v", err)
			}
		})
	}
}

func TestWriteConfigReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r1.cfg")

	if err := os.WriteFile(p, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := writeConfig(dir, "r1", "new"); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	if b, _ := os.ReadFile(p); string(b) != "new\n" {
		t.Errorf("content = %q", b)
	}

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only r1.cfg", len(entries))
	}
}

func TestWriteConfigReplacesSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")

	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(dir, "r1.cfg")
	if err := os.Symlink(victim, p); err != nil {
		t.Fatal(err)
	}

	if _, err := writeConfig(dir, "r1", "new"); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}

	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, want regular file 0600", p, fi.Mode())
	}

	if b, _ := os.ReadFile(p); string(b) != "new\n" {
		t.Errorf("content = %q", b)
	}

	if b, _ := os.ReadFile(victim); string(b) != "keep\n" {
		t.Errorf("symlink target changed: %q", b)
	}
}

func TestHelpDoesNotShowEnvUsername(t *testing.T) {
	t.Setenv("COMET_USERNAME", "envuser1")
	t.Setenv("USER", "envuser2")

	var out bytes.Buffer

	cmd := newRootCommand(&globalOptions{stdout: &out, stderr: &out})
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if strings.Contains(got, "envuser") {
		t.Errorf("help shows environment username:\n%s", got)
	}

	if !strings.Contains(got, "$COMET_USERNAME, then $USER") {
		t.Errorf("help does not describe the username fallback:\n%s", got)
	}
}

func TestUsernameFallback(t *testing.T) {
	inv := writeInventory(t, `
devices:
  - {name: a, host: 192.0.2.1, platform: eos, username: admin}
  - {name: b, host: 192.0.2.2, platform: eos}
`)

	cases := map[string]struct {
		env   map[string]string
		flags []string
		want  string
	}{
		"COMET_USERNAME first": {map[string]string{"COMET_USERNAME": "comet", "USER": "user"}, nil, "comet"},
		"then USER":            {map[string]string{"COMET_USERNAME": "", "USER": "user"}, nil, "user"},
		"flag over env":        {map[string]string{"COMET_USERNAME": "comet", "USER": "user"}, []string{"-u", "flag"}, "flag"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := harness{env: tc.env}

			if err := h.execute(t, append([]string{"exec", "-i", inv, "-c", "x"}, tc.flags...)...); err != nil {
				t.Fatalf("exec: %v", err)
			}

			got := map[string]string{}
			for _, tgt := range h.targets {
				got[tgt.Name] = tgt.Username
			}

			if got["a"] != "admin" || got["b"] != tc.want {
				t.Errorf("usernames = %v, want a=admin b=%s", got, tc.want)
			}
		})
	}
}

// --workers bounds how many devices are open at once, and output stays in
// inventory order even when earlier devices finish last.
func TestWorkersBoundConcurrencyAndKeepOrder(t *testing.T) {
	h := harness{delay: map[string]time.Duration{
		"d1": 60 * time.Millisecond,
		"d2": 40 * time.Millisecond,
		"d3": 20 * time.Millisecond,
		"d4": 0,
	}}

	inv := writeInventory(t, `
devices:
  - {name: d1, host: 192.0.2.1, platform: eos}
  - {name: d2, host: 192.0.2.2, platform: eos}
  - {name: d3, host: 192.0.2.3, platform: eos}
  - {name: d4, host: 192.0.2.4, platform: eos}
`)

	if err := h.execute(t, "exec", "-i", inv, "-c", "x", "-w", "2"); err != nil {
		t.Fatalf("exec: %v", err)
	}

	if peak := h.peak.Load(); peak != 2 {
		t.Errorf("peak concurrent sessions = %d, want 2", peak)
	}

	want := ""
	for _, n := range []string{"d1", "d2", "d3", "d4"} {
		want += "==> " + n + " <==\n--- x\n" + n + " x\n"
	}

	if got := h.stdout.String(); got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
}

// Names that collide on a case-insensitive file system, or are too long for a
// file name, stop the run before any device is contacted.
func TestConfigGetRejectsCollidingAndLongNames(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"case collision": {`
devices:
  - {name: R1, host: 192.0.2.1, platform: eos}
  - {name: r1, host: 192.0.2.2, platform: eos}
`, "differ only in case"},
		"too long": {"devices: [{name: " + strings.Repeat("a", 252) + ", host: 192.0.2.1, platform: eos}]", "cannot be used as a file name"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var h harness

			err := h.execute(t, "config", "get", "-i", writeInventory(t, tc.doc), "-o", t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}

			if n := h.opens.Load(); n != 0 {
				t.Errorf("%d devices contacted", n)
			}
		})
	}
}

// An output directory that cannot be written stops the run before any device
// is contacted.
func TestConfigGetUnwritableOutputDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}

	var h harness

	out := t.TempDir()
	if err := os.Chmod(out, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(out, 0o700) })

	err := h.execute(t, "config", "get", "-i", writeInventory(t, twoDevices), "-o", out)
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("error = %v, want not writable", err)
	}

	if n := h.opens.Load(); n != 0 {
		t.Errorf("%d devices contacted", n)
	}
}
