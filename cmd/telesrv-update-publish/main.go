// Command telesrv-update-publish validates and atomically publishes an update
// catalog. It never changes the package files referenced by the catalog.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"telesrv/internal/updatecdn"
)

func main() {
	candidate := flag.String("manifest", "", "candidate manifest to validate and publish")
	active := flag.String("active", "data/updates/manifest.json", "active manifest path")
	files := flag.String("files", "data/updates/files", "directory containing published packages")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(errors.New("positional arguments are not accepted"))
	}
	if *candidate == "" {
		fatal(errors.New("-manifest is required"))
	}
	if err := publish(*candidate, *active, *files); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "telesrv-update-publish:", err)
	os.Exit(1)
}

func publish(candidate, active, files string) error {
	candidatePath, err := filepath.Abs(candidate)
	if err != nil {
		return fmt.Errorf("resolve candidate: %w", err)
	}
	activePath, err := filepath.Abs(active)
	if err != nil {
		return fmt.Errorf("resolve active manifest: %w", err)
	}
	filesPath, err := filepath.Abs(files)
	if err != nil {
		return fmt.Errorf("resolve files directory: %w", err)
	}
	if candidatePath == activePath {
		return errors.New("candidate and active manifest must be different files")
	}
	if _, err := updatecdn.LoadCatalog(candidatePath, filesPath); err != nil {
		return fmt.Errorf("validate candidate: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(activePath), 0o700); err != nil {
		return fmt.Errorf("create active manifest directory: %w", err)
	}
	if err := os.Rename(candidatePath, activePath); err != nil {
		return fmt.Errorf("atomically publish manifest: %w", err)
	}
	fmt.Printf("published validated update manifest to %s\n", activePath)
	return nil
}