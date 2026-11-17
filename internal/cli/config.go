// Copyright 2026 Datum Cloud, Inc.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"go.datum.net/comet/internal/driver"
)

func newConfigCommand(o *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Work with device configurations",
	}

	cmd.AddCommand(newConfigGetCommand(o))

	return cmd
}

func newConfigGetCommand(o *globalOptions) *cobra.Command {
	var outDir string

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Collect running configurations",
		Long: `Collect each device's running configuration in its native text format
(Junos: "show configuration", EOS: "show running-config").

With --output-dir, each configuration is written to <output-dir>/<name>.cfg
with mode 0600. Without it, a single device's configuration is printed to
stdout.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runConfigGet(o, outDir)
		},
	}

	cmd.Flags().StringVarP(&outDir, "output-dir", "o", "", "directory to write <name>.cfg files to")

	return cmd
}

func runConfigGet(o *globalOptions, outDir string) error {
	targets, err := o.targets()
	if err != nil {
		return err
	}

	if outDir == "" {
		if len(targets) != 1 {
			return errors.New("--output-dir is required when collecting from more than one device")
		}
	} else {
		// Case-insensitive file systems (the macOS default) map names that
		// differ only in case to the same file.
		seen := map[string]string{}

		for _, t := range targets {
			if _, err := configFileName(t.Name); err != nil {
				return err
			}

			if prev, ok := seen[strings.ToLower(t.Name)]; ok {
				return fmt.Errorf("device names %q and %q differ only in case and would write the same file", prev, t.Name)
			}

			seen[strings.ToLower(t.Name)] = t.Name
		}
	}

	p, err := o.newPlan(targets)
	if err != nil {
		return err
	}

	if outDir != "" {
		if err := checkOutputDir(outDir); err != nil {
			return err
		}
	}

	results := run(o, p, func(s driver.Session) (string, error) {
		return s.GetConfig()
	})

	for i, r := range results {
		if r.err != nil {
			continue
		}

		if outDir == "" {
			if _, err := fmt.Fprint(o.stdout, ensureNewline(r.value)); err != nil {
				return err
			}

			continue
		}

		path, err := writeConfig(outDir, r.target.Name, r.value)
		if err != nil {
			results[i].err = err

			continue
		}

		_, _ = fmt.Fprintf(o.stderr, "%s: wrote %s\n", r.target.Name, path)
	}

	return reportFailures(o, results)
}

// configFileName returns the file name for a device's configuration, <name>.cfg.
// Names that would escape the output directory are rejected.
func configFileName(name string) (string, error) {
	file := name + ".cfg"

	// 255 bytes is the file name limit on Linux and macOS file systems.
	if name == "" || name == "." || name == ".." || len(file) > 255 ||
		strings.ContainsAny(name, "/\\\x00") {
		return "", fmt.Errorf("device name %q cannot be used as a file name", name)
	}

	return file, nil
}

// checkOutputDir creates dir and checks that a file can be written in it, so
// an unusable --output-dir stops the run before any device is contacted.
func checkOutputDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	f, err := os.CreateTemp(dir, ".comet-probe-*")
	if err != nil {
		return fmt.Errorf("output directory %q is not writable: %w", dir, err)
	}

	_ = f.Close()

	return os.Remove(f.Name())
}

// writeConfig atomically replaces dir/<name>.cfg with cfg, with mode 0600. The
// configuration is written to a temporary file in dir and renamed over the
// target, so an interrupted write leaves any previous file intact and a
// symlink at the target is replaced rather than followed.
func writeConfig(dir, name, cfg string) (string, error) {
	file, err := configFileName(name)
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, file)

	f, err := os.CreateTemp(dir, "."+file+".*.tmp")
	if err != nil {
		return "", err
	}

	tmp := f.Name()

	_, err = f.WriteString(ensureNewline(cfg))
	if err == nil {
		err = f.Chmod(0o600)
	}

	if err == nil {
		err = f.Sync()
	}

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err == nil {
		err = os.Rename(tmp, path)
	}

	if err != nil {
		_ = os.Remove(tmp)

		return "", err
	}

	return path, nil
}

func ensureNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}

	return s + "\n"
}
