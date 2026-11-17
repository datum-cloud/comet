// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"go.datum.net/comet/internal/driver"
)

func newExecCommand(o *globalOptions) *cobra.Command {
	var commands []string

	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Run operational commands and print their output",
		Long: `Run one or more operational CLI commands on each device, in order, and
print the output to stdout. When more than one device is targeted, each
device's output is preceded by a "==> name <==" header.`,
		Example: `  comet exec --host 192.0.2.1 --platform eos -c "show version"
  comet exec -i inventory.yaml -c "show version" -c "show interfaces terse"`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if len(commands) == 0 {
				return errors.New("at least one --command is required")
			}

			return runExec(o, commands)
		},
	}

	cmd.Flags().StringArrayVarP(&commands, "command", "c", nil, "command to run (repeatable)")

	return cmd
}

func runExec(o *globalOptions, commands []string) error {
	targets, err := o.targets()
	if err != nil {
		return err
	}

	p, err := o.newPlan(targets)
	if err != nil {
		return err
	}

	results := run(o, p, func(s driver.Session) ([]driver.Output, error) {
		return s.RunCommands(commands)
	})

	for _, r := range results {
		if len(results) > 1 {
			if _, err := fmt.Fprintf(o.stdout, "==> %s <==\n", r.target.Name); err != nil {
				return err
			}
		}

		for _, out := range r.value {
			if _, err := fmt.Fprintf(o.stdout, "--- %s\n%s", out.Command, ensureNewline(out.Result)); err != nil {
				return err
			}
		}
	}

	return reportFailures(o, results)
}
