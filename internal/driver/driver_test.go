// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package driver

import (
	"strings"
	"testing"
)

func TestNewUnknownTransport(t *testing.T) {
	Register("test-only", func(Target) (Session, error) { return nil, nil })

	_, err := New(Target{Transport: "gnmi"})
	if err == nil || !strings.Contains(err.Error(), `unsupported transport "gnmi"`) ||
		!strings.Contains(err.Error(), "test-only") {
		t.Fatalf("New error = %v", err)
	}
}
