// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package inventory loads the YAML device inventory.
package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Device is one inventory entry. Optional fields left empty take their value
// from command-line flags.
type Device struct {
	Name      string `yaml:"name"`
	Host      string `yaml:"host"`
	Platform  string `yaml:"platform"`
	Port      int    `yaml:"port"`
	Transport string `yaml:"transport"`
	Username  string `yaml:"username"`
	SSHKey    string `yaml:"ssh_key"`
}

// Inventory is the top-level inventory document.
type Inventory struct {
	Devices []Device `yaml:"devices"`
}

// Load reads and validates the inventory at path. Unknown keys are rejected
// so that typos do not silently fall back to defaults.
func Load(path string) (*Inventory, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is the operator's --inventory argument.
	if err != nil {
		return nil, err
	}

	return Parse(b)
}

// Parse decodes and validates an inventory document. A port of 0 means unset.
func Parse(b []byte) (*Inventory, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)

	var inv Inventory
	if err := dec.Decode(&inv); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("inventory is empty")
		}

		return nil, fmt.Errorf("parsing inventory: %w", err)
	}

	if len(inv.Devices) == 0 {
		return nil, errors.New("inventory has no devices")
	}

	for i, d := range inv.Devices {
		if d.Host == "" {
			return nil, fmt.Errorf("device %d (%q): host is required", i, d.Name)
		}

		if d.Platform == "" {
			return nil, fmt.Errorf("device %d (%q): platform is required", i, d.Name)
		}

		if d.Port < 0 || d.Port > 65535 {
			return nil, fmt.Errorf("device %d (%q): port %d is out of range 1-65535", i, d.Name, d.Port)
		}

		if d.Name == "" {
			inv.Devices[i].Name = d.Host
		}
	}

	return &inv, nil
}
