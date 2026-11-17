// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package inventory

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	inv, err := Parse([]byte(`
devices:
  - name: core1
    host: 192.0.2.1
    platform: junos
    port: 2222
    username: ops
    ssh_key: ~/.ssh/id_ed25519
  - host: 192.0.2.2
    platform: eos
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []Device{
		{Name: "core1", Host: "192.0.2.1", Platform: "junos", Port: 2222, Username: "ops", SSHKey: "~/.ssh/id_ed25519"},
		{Name: "192.0.2.2", Host: "192.0.2.2", Platform: "eos"},
	}

	if len(inv.Devices) != len(want) {
		t.Fatalf("got %d devices, want %d", len(inv.Devices), len(want))
	}

	for i := range want {
		if inv.Devices[i] != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, inv.Devices[i], want[i])
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"empty":            {"devices: []", "no devices"},
		"empty file":       {"", "inventory is empty"},
		"comments only":    {"# nothing here\n", "inventory is empty"},
		"port too high":    {"devices: [{host: 192.0.2.1, platform: eos, port: 65536}]", "out of range"},
		"negative port":    {"devices: [{host: 192.0.2.1, platform: eos, port: -1}]", "out of range"},
		"missing host":     {"devices: [{name: a, platform: eos}]", "host is required"},
		"missing platform": {"devices: [{host: 192.0.2.1}]", "platform is required"},
		"unknown field":    {"devices: [{host: 192.0.2.1, platform: eos, pasword: x}]", "pasword"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse error = %v, want containing %q", err, tc.want)
			}
		})
	}
}
