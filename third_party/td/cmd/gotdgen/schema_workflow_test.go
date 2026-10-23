package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/tl"

	"github.com/iamxvbaba/td/gen"
	"github.com/iamxvbaba/td/gen/semantic"
)

func TestRunSchemaImportUpdatesLockedUniverseAndCanonicalSchema(t *testing.T) {
	root := t.TempDir()
	manifestRoot := filepath.Join(root, "_schema", "layers")
	if err := os.MkdirAll(manifestRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	layer1 := []byte("---types---\nold#10000001 value:int = Sample;\n// LAYER 1\n")
	layer2 := []byte("---types---\nmodern#10000002 value:int = Sample;\n// LAYER 2\n")
	overlay := []byte("---types---\nlegacy#10000003 = Legacy;\n")
	artifact1, err := semantic.InspectLayerArtifact(layer1)
	if err != nil {
		t.Fatal(err)
	}
	overlaySHA := sha256.Sum256(overlay)
	manifest := semantic.Manifest{
		CanonicalLayer: 1,
		Repository:     "https://github.com/example/schema.git",
		SourcePath:     "api.tl",
		Overlays: []semantic.ManifestOverlay{{
			File:       "../legacy.tl",
			Repository: "https://github.com/example/overlay.git",
			Commit:     strings.Repeat("a", 40),
			Blob:       strings.Repeat("b", 40),
			Path:       "_schema/legacy.tl",
			SHA256:     hex.EncodeToString(overlaySHA[:]),
		}},
		Layers: []semantic.ManifestLayer{{
			Layer:  1,
			File:   "layer-1.tl",
			Commit: strings.Repeat("c", 40),
			Blob:   artifact1.GitBlob,
			SHA256: artifact1.SHA256,
		}},
	}
	manifestData, err := semantic.MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(manifestRoot, "manifest.json")
	writeTestFile(t, filepath.Join(manifestRoot, "layer-1.tl"), layer1)
	writeTestFile(t, filepath.Join(root, "_schema", "legacy.tl"), overlay)
	writeTestFile(t, manifestPath, manifestData)
	upstream := filepath.Join(root, "upstream")
	if err := os.Mkdir(upstream, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(upstream, "init", "--quiet"); err != nil {
		t.Skipf("local git is unavailable: %v", err)
	}
	if _, err := runGit(upstream, "remote", "add", "origin", "https://github.com/example/schema.git"); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(upstream, "api.tl")
	writeTestFile(t, sourcePath, layer2)
	writeTestFile(t, filepath.Join(upstream, ".gitattributes"), []byte("*.tl text eol=lf\n"))
	if _, err := runGit(upstream, "add", "api.tl", ".gitattributes"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(upstream, "-c", "user.name=gotdgen-test", "-c", "user.email=gotdgen@example.invalid", "commit", "--quiet", "-m", "layer 2"); err != nil {
		t.Fatal(err)
	}
	commit, err := runGit(upstream, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// Import provenance is accepted only from a fetched ref of the configured
	// upstream remote, not merely from a local commit plus a matching URL.
	if _, err := runGit(upstream, "update-ref", "refs/remotes/origin/main", commit); err != nil {
		t.Fatal(err)
	}
	// Simulate a normal Windows CRLF worktree. Import must lock and copy the
	// exact Git blob bytes, not the platform checkout representation.
	writeTestFile(t, sourcePath, bytes.ReplaceAll(layer2, []byte("\n"), []byte("\r\n")))
	canonicalPath := filepath.Join(root, "_schema", "telegram.tl")

	entry, err := runSchemaImport(schemaImportOptions{
		ManifestPath:    manifestPath,
		SourcePath:      sourcePath,
		Canonical:       true,
		CanonicalOutput: canonicalPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Layer != 2 || entry.File != "layer-2.tl" || entry.Commit != commit {
		t.Fatalf("imported entry = %+v", entry)
	}
	locked, err := semantic.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if locked.CanonicalLayer != 2 || len(locked.Layers) != 2 {
		t.Fatalf("locked manifest = %+v", locked)
	}
	if got, err := os.ReadFile(filepath.Join(manifestRoot, "layer-2.tl")); err != nil || !bytes.Equal(got, layer2) {
		t.Fatalf("imported layer bytes equal=%v error=%v", bytes.Equal(got, layer2), err)
	}
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := tl.Parse(bytes.NewReader(canonical))
	if err != nil {
		t.Fatalf("parse canonical schema: %v", err)
	}
	if parsed.Layer != 2 || len(parsed.Definitions) != 2 {
		t.Fatalf("canonical schema = layer %d, definitions %d", parsed.Layer, len(parsed.Definitions))
	}
	if _, err := semantic.LoadUniverse(manifestPath); err != nil {
		t.Fatalf("load imported universe: %v", err)
	}

	// A rerun from the same local artifact and provenance is reproducible and
	// does not require replace mode.
	beforeManifest, _ := os.ReadFile(manifestPath)
	beforeCanonical, _ := os.ReadFile(canonicalPath)
	if _, err := runSchemaImport(schemaImportOptions{
		ManifestPath:    manifestPath,
		SourcePath:      sourcePath,
		Canonical:       true,
		CanonicalOutput: canonicalPath,
	}); err != nil {
		t.Fatalf("idempotent import: %v", err)
	}
	if after, _ := os.ReadFile(manifestPath); !bytes.Equal(after, beforeManifest) {
		t.Fatal("idempotent import changed manifest")
	}
	if after, _ := os.ReadFile(canonicalPath); !bytes.Equal(after, beforeCanonical) {
		t.Fatal("idempotent import changed canonical schema")
	}

	// A custom canonical target must not overwrite any locked input. In
	// particular, the transaction's output-overlap check cannot protect an
	// existing historical Layer or overlay because neither is otherwise an
	// output of this import.
	policyPath := filepath.Join(manifestRoot, "policy.json")
	writeTestFile(t, policyPath, []byte("locked policy"))
	for _, protected := range []struct {
		name string
		path string
	}{
		{"manifest", manifestPath},
		{"imported_layer", filepath.Join(manifestRoot, "layer-2.tl")},
		{"historical_layer", filepath.Join(manifestRoot, "layer-1.tl")},
		{"overlay", filepath.Join(root, "_schema", "legacy.tl")},
		{"policy", policyPath},
		{"upstream_source", sourcePath},
	} {
		t.Run("reject_canonical_overwrite_"+protected.name, func(t *testing.T) {
			before, err := os.ReadFile(protected.path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runSchemaImport(schemaImportOptions{
				ManifestPath:    manifestPath,
				SourcePath:      sourcePath,
				Canonical:       true,
				CanonicalOutput: protected.path,
				PolicyPath:      policyPath,
			})
			if err == nil || !strings.Contains(err.Error(), "canonical schema output must not overwrite") {
				t.Fatalf("protected canonical output error = %v", err)
			}
			after, readErr := os.ReadFile(protected.path)
			if readErr != nil || !bytes.Equal(after, before) {
				t.Fatalf("rejected canonical output changed %s: equal=%v error=%v", protected.path, bytes.Equal(after, before), readErr)
			}
		})
	}
	if after, _ := os.ReadFile(manifestPath); !bytes.Equal(after, beforeManifest) {
		t.Fatal("rejected canonical output changed manifest")
	}
	if after, _ := os.ReadFile(canonicalPath); !bytes.Equal(after, beforeCanonical) {
		t.Fatal("rejected canonical output changed canonical schema")
	}

	// Replacing the Layer which remains canonical must regenerate the canonical
	// companion even when the caller explicitly declines to change canonical
	// selection.
	replacementLayer2 := []byte("---types---\nmodern#10000005 value:long = Sample;\n// LAYER 2\n")
	writeTestFile(t, sourcePath, replacementLayer2)
	if _, err := runGit(upstream, "add", "api.tl"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(upstream, "-c", "user.name=gotdgen-test", "-c", "user.email=gotdgen@example.invalid", "commit", "--quiet", "-m", "replace layer 2"); err != nil {
		t.Fatal(err)
	}
	replacementCommit, err := runGit(upstream, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(upstream, "update-ref", "refs/remotes/origin/main", replacementCommit); err != nil {
		t.Fatal(err)
	}
	replaced, err := runSchemaImport(schemaImportOptions{
		ManifestPath:    manifestPath,
		SourcePath:      sourcePath,
		Canonical:       false,
		Replace:         true,
		CanonicalOutput: canonicalPath,
		PolicyPath:      policyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Layer != 2 || replaced.Commit != replacementCommit {
		t.Fatalf("replaced canonical entry = %+v", replaced)
	}
	rebuiltCanonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(rebuiltCanonical, beforeCanonical) {
		t.Fatal("canonical=false replacement left stale canonical schema")
	}
	rebuiltSchema, err := tl.Parse(bytes.NewReader(rebuiltCanonical))
	if err != nil {
		t.Fatal(err)
	}
	foundReplacement := false
	for _, definition := range rebuiltSchema.Definitions {
		if definition.Definition.Name == "modern" && definition.Definition.ID == 0x10000005 {
			foundReplacement = true
			break
		}
	}
	if rebuiltSchema.Layer != 2 || !foundReplacement {
		t.Fatalf("rebuilt canonical schema = layer %d, replacement=%v", rebuiltSchema.Layer, foundReplacement)
	}
	locked, err = semantic.ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if locked.CanonicalLayer != 2 {
		t.Fatalf("canonical=false replacement changed canonical selection to %d", locked.CanonicalLayer)
	}
	beforeManifest, _ = os.ReadFile(manifestPath)
	beforeCanonical = rebuiltCanonical

	// A subsequent local-only schema commit must not be attributed to origin.
	layer3 := []byte("---types---\nfuture#10000004 value:int = Sample;\n// LAYER 3\n")
	writeTestFile(t, sourcePath, layer3)
	if _, err := runGit(upstream, "add", "api.tl"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(upstream, "-c", "user.name=gotdgen-test", "-c", "user.email=gotdgen@example.invalid", "commit", "--quiet", "-m", "local-only layer 3"); err != nil {
		t.Fatal(err)
	}
	if _, err := runSchemaImport(schemaImportOptions{
		ManifestPath:    manifestPath,
		SourcePath:      sourcePath,
		Canonical:       true,
		CanonicalOutput: canonicalPath,
	}); err == nil || !strings.Contains(err.Error(), "not reachable from any fetched ref") {
		t.Fatalf("local-only provenance error = %v", err)
	}
	if after, _ := os.ReadFile(manifestPath); !bytes.Equal(after, beforeManifest) {
		t.Fatal("rejected local-only import changed manifest")
	}
	if after, _ := os.ReadFile(canonicalPath); !bytes.Equal(after, beforeCanonical) {
		t.Fatal("rejected local-only import changed canonical schema")
	}
}

func TestSchemaImportTransactionCommitsManifestLast(t *testing.T) {
	root := t.TempDir()
	layerPath := filepath.Join(root, "layer-2.tl")
	canonicalPath := filepath.Join(root, "telegram.tl")
	manifestPath := filepath.Join(root, "manifest.json")
	for path, data := range map[string][]byte{
		layerPath:     []byte("old layer"),
		canonicalPath: []byte("old canonical"),
		manifestPath:  []byte("old manifest"),
	} {
		writeTestFile(t, path, data)
	}

	var replacements []string
	err := commitSchemaImportTransactionWithReplace(
		[]schemaImportTransactionFile{
			{Label: "imported layer", Path: layerPath, Data: []byte("new layer"), Perm: 0o644},
			{Label: "canonical schema", Path: canonicalPath, Data: []byte("new canonical"), Perm: 0o644},
		},
		schemaImportTransactionFile{Label: "schema manifest", Path: manifestPath, Data: []byte("new manifest"), Perm: 0o644},
		func(source, destination string) error {
			replacements = append(replacements, filepath.Base(destination))
			return os.Rename(source, destination)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"layer-2.tl", "telegram.tl", "manifest.json"}
	if strings.Join(replacements, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("replacement order = %v, want %v", replacements, wantOrder)
	}
	assertFileContent(t, layerPath, "new layer")
	assertFileContent(t, canonicalPath, "new canonical")
	assertFileContent(t, manifestPath, "new manifest")
	assertNoSchemaImportTemporaryFiles(t, layerPath, canonicalPath, manifestPath)
}

func TestSchemaImportTransactionRollsBackEveryReplacementFailure(t *testing.T) {
	for failAt := 1; failAt <= 3; failAt++ {
		for _, failAfterReplace := range []bool{false, true} {
			name := fmt.Sprintf("replace_%d", failAt)
			if failAfterReplace {
				name += "_after_side_effect"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				layerPath := filepath.Join(root, "layer-2.tl")
				canonicalPath := filepath.Join(root, "telegram.tl")
				manifestPath := filepath.Join(root, "manifest.json")
				for path, data := range map[string][]byte{
					layerPath:     []byte("old layer"),
					canonicalPath: []byte("old canonical"),
					manifestPath:  []byte("old manifest"),
				} {
					writeTestFile(t, path, data)
				}

				injected := errors.New("injected replacement failure")
				attempt := 0
				err := commitSchemaImportTransactionWithReplace(
					[]schemaImportTransactionFile{
						{Label: "imported layer", Path: layerPath, Data: []byte("new layer"), Perm: 0o644},
						{Label: "canonical schema", Path: canonicalPath, Data: []byte("new canonical"), Perm: 0o644},
					},
					schemaImportTransactionFile{Label: "schema manifest", Path: manifestPath, Data: []byte("new manifest"), Perm: 0o644},
					func(source, destination string) error {
						attempt++
						if attempt == failAt {
							if failAfterReplace {
								if err := os.Rename(source, destination); err != nil {
									return err
								}
							}
							return injected
						}
						return os.Rename(source, destination)
					},
				)
				if !errors.Is(err, injected) {
					t.Fatalf("transaction error = %v, want injected failure", err)
				}
				assertFileContent(t, layerPath, "old layer")
				assertFileContent(t, canonicalPath, "old canonical")
				assertFileContent(t, manifestPath, "old manifest")
				assertNoSchemaImportTemporaryFiles(t, layerPath, canonicalPath, manifestPath)
			})
		}
	}
}

func TestSchemaImportTransactionRemovesNewLayerOnRollback(t *testing.T) {
	root := t.TempDir()
	layerPath := filepath.Join(root, "layer-2.tl")
	canonicalPath := filepath.Join(root, "telegram.tl")
	manifestPath := filepath.Join(root, "manifest.json")
	writeTestFile(t, canonicalPath, []byte("old canonical"))
	writeTestFile(t, manifestPath, []byte("old manifest"))

	injected := errors.New("fail manifest replacement")
	attempt := 0
	err := commitSchemaImportTransactionWithReplace(
		[]schemaImportTransactionFile{
			{Label: "imported layer", Path: layerPath, Data: []byte("new layer"), Perm: 0o644},
			{Label: "canonical schema", Path: canonicalPath, Data: []byte("new canonical"), Perm: 0o644},
		},
		schemaImportTransactionFile{Label: "schema manifest", Path: manifestPath, Data: []byte("new manifest"), Perm: 0o644},
		func(source, destination string) error {
			attempt++
			if attempt == 3 {
				return injected
			}
			return os.Rename(source, destination)
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("transaction error = %v, want injected failure", err)
	}
	if _, err := os.Stat(layerPath); !os.IsNotExist(err) {
		t.Fatalf("new layer survived rollback: %v", err)
	}
	assertFileContent(t, canonicalPath, "old canonical")
	assertFileContent(t, manifestPath, "old manifest")
	assertNoSchemaImportTemporaryFiles(t, layerPath, canonicalPath, manifestPath)
}

func TestSchemaImportTransactionStagesCompleteSetBeforeCommit(t *testing.T) {
	root := t.TempDir()
	layerPath := filepath.Join(root, "layer-2.tl")
	manifestPath := filepath.Join(root, "manifest.json")
	writeTestFile(t, layerPath, []byte("old layer"))
	writeTestFile(t, manifestPath, []byte("old manifest"))
	missingCanonicalPath := filepath.Join(root, "missing", "telegram.tl")

	err := commitSchemaImportTransaction(
		[]schemaImportTransactionFile{
			{Label: "imported layer", Path: layerPath, Data: []byte("new layer"), Perm: 0o644},
			{Label: "canonical schema", Path: missingCanonicalPath, Data: []byte("new canonical"), Perm: 0o644},
		},
		schemaImportTransactionFile{Label: "schema manifest", Path: manifestPath, Data: []byte("new manifest"), Perm: 0o644},
	)
	if err == nil || !strings.Contains(err.Error(), "stage canonical schema") {
		t.Fatalf("stage error = %v", err)
	}
	assertFileContent(t, layerPath, "old layer")
	assertFileContent(t, manifestPath, "old manifest")
	if _, err := os.Stat(missingCanonicalPath); !os.IsNotExist(err) {
		t.Fatalf("canonical target created during failed staging: %v", err)
	}
	assertNoSchemaImportTemporaryFiles(t, layerPath, manifestPath)
}

func TestRunLayerPolicyAuditWritesStaleReportAndSafeMerge(t *testing.T) {
	root := t.TempDir()
	source := []byte("---types---\nsample#10000001 = Sample;\n// LAYER 1\n")
	artifact, err := semantic.InspectLayerArtifact(source)
	if err != nil {
		t.Fatal(err)
	}
	manifest := semantic.Manifest{
		CanonicalLayer: 1,
		Repository:     "https://example.invalid/schema.git",
		SourcePath:     "api.tl",
		Layers: []semantic.ManifestLayer{{
			Layer:  1,
			File:   "layer-1.tl",
			Commit: strings.Repeat("a", 40),
			Blob:   artifact.GitBlob,
			SHA256: artifact.SHA256,
		}},
	}
	manifestData, err := semantic.MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	writeTestFile(t, filepath.Join(root, "layer-1.tl"), source)
	writeTestFile(t, manifestPath, manifestData)
	policyData, err := gen.MarshalLayerPolicyDocument(gen.LayerPolicyDocument{
		Version: gen.LayerPolicyVersion,
		Entries: []gen.LayerObligationPolicyEntry{{
			Key:        "layer-obligation/v1/required/stale-shape",
			Resolution: gen.LayerObligationResolution{Action: gen.LayerResolveReject},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, "policy.json")
	auditPath := filepath.Join(root, "policy.audit.json")
	mergePath := filepath.Join(root, "policy.next.json")
	writeTestFile(t, policyPath, policyData)

	audit, err := runLayerPolicyAudit(manifestPath, policyPath, auditPath, mergePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Retained) != 0 || len(audit.Stale) != 1 || len(audit.New) != 0 {
		t.Fatalf("policy audit = %+v", audit)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil || !bytes.Contains(data, []byte(`"stale"`)) || !bytes.Contains(data, []byte("stale-shape")) {
		t.Fatalf("audit output = %s, error=%v", data, err)
	}
	merged, err := gen.LoadLayerPolicy(mergePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Entries) != 0 {
		t.Fatalf("safe merged policy retained stale entries: %+v", merged.Entries)
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}

func assertNoSchemaImportTemporaryFiles(t *testing.T, targets ...string) {
	t.Helper()
	for _, target := range targets {
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".schema-import-*-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("schema import left temporary files for %s: %v", target, matches)
		}
	}
}
