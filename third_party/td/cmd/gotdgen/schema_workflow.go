package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iamxvbaba/td/gen"
	"github.com/iamxvbaba/td/gen/semantic"
)

type schemaImportOptions struct {
	ManifestPath    string
	SourcePath      string
	GitDir          string
	Commit          string
	ExpectedBlob    string
	Canonical       bool
	Replace         bool
	CanonicalOutput string
	PolicyPath      string
}

type schemaImportCanonicalPlan struct {
	Rebuild bool
	Path    string
}

func runSchemaImport(options schemaImportOptions) (semantic.ManifestLayer, error) {
	manifest, err := semantic.ReadManifest(options.ManifestPath)
	if err != nil {
		return semantic.ManifestLayer{}, err
	}
	// Refuse to build on an already-invalid universe. The import workflow may
	// extend a reviewed universe, but never normalizes an earlier bad state.
	if _, err := semantic.LoadUniverse(options.ManifestPath); err != nil {
		return semantic.ManifestLayer{}, fmt.Errorf("validate existing schema universe: %w", err)
	}
	provenance, err := loadVerifiedSchemaSource(options.SourcePath, options.GitDir, manifest.Repository, manifest.SourcePath, options.Commit)
	if err != nil {
		return semantic.ManifestLayer{}, err
	}
	artifact, err := semantic.InspectLayerArtifact(provenance.Source)
	if err != nil {
		return semantic.ManifestLayer{}, err
	}
	if artifact.GitBlob != provenance.Blob {
		return semantic.ManifestLayer{}, fmt.Errorf("verified Git blob %s produced artifact blob %s", provenance.Blob, artifact.GitBlob)
	}
	if options.ExpectedBlob != "" && options.ExpectedBlob != artifact.GitBlob {
		return semantic.ManifestLayer{}, fmt.Errorf("imported schema Git blob mismatch: got %s, want %s", artifact.GitBlob, options.ExpectedBlob)
	}
	file := fmt.Sprintf("layer-%d.tl", artifact.Layer)
	updated, err := semantic.UpdateManifestLayer(manifest, artifact, provenance.Commit, file, options.Canonical, options.Replace)
	if err != nil {
		return semantic.ManifestLayer{}, err
	}
	manifestData, err := semantic.MarshalManifest(updated)
	if err != nil {
		return semantic.ManifestLayer{}, err
	}

	manifestRoot := filepath.Dir(options.ManifestPath)
	if _, err := semantic.LoadManifestUniverse(updated, manifestRoot, map[string][]byte{file: artifact.Source}); err != nil {
		return semantic.ManifestLayer{}, fmt.Errorf("validate imported schema universe: %w", err)
	}
	destination := filepath.Join(manifestRoot, file)
	canonicalPlan, err := planSchemaImportCanonicalOutput(options, updated, manifestRoot, destination, artifact.Layer)
	if err != nil {
		return semantic.ManifestLayer{}, err
	}
	var canonicalPath string
	var canonicalData []byte
	if canonicalPlan.Rebuild {
		canonicalPath = canonicalPlan.Path
		overlays := make(map[string][]byte, len(updated.Overlays))
		for _, overlay := range updated.Overlays {
			data, err := os.ReadFile(filepath.Join(manifestRoot, filepath.FromSlash(overlay.File)))
			if err != nil {
				return semantic.ManifestLayer{}, fmt.Errorf("read canonical overlay %q: %w", overlay.File, err)
			}
			overlays[overlay.File] = data
		}
		canonicalData, err = semantic.RenderCanonicalSchema(updated, artifact.Source, overlays)
		if err != nil {
			return semantic.ManifestLayer{}, err
		}
	}

	var imported semantic.ManifestLayer
	foundImported := false
	for _, layer := range updated.Layers {
		if layer.Layer == artifact.Layer {
			imported = layer
			foundImported = true
			break
		}
	}
	if !foundImported {
		return semantic.ManifestLayer{}, fmt.Errorf("imported layer %d disappeared from manifest", artifact.Layer)
	}

	// All parsing, hashing, provenance checks and canonical rendering complete
	// before the first tracked file changes. The data files are committed first
	// and the manifest is committed last as the transaction marker. A failed
	// replacement restores the complete previous set.
	dataFiles := []schemaImportTransactionFile{{
		Label: "imported layer",
		Path:  destination,
		Data:  artifact.Source,
		Perm:  0o644,
	}}
	if canonicalPlan.Rebuild {
		dataFiles = append(dataFiles, schemaImportTransactionFile{
			Label: "canonical schema",
			Path:  canonicalPath,
			Data:  canonicalData,
			Perm:  0o644,
		})
	}
	if err := commitSchemaImportTransaction(dataFiles, schemaImportTransactionFile{
		Label: "schema manifest",
		Path:  options.ManifestPath,
		Data:  manifestData,
		Perm:  0o644,
	}); err != nil {
		return semantic.ManifestLayer{}, err
	}
	return imported, nil
}

// planSchemaImportCanonicalOutput derives canonical regeneration from the
// resulting manifest, not merely from the caller's selection flag. Replacing
// the layer which remains canonical must rebuild the companion schema even
// when --schema-import-canonical=false was used to avoid changing selection.
func planSchemaImportCanonicalOutput(
	options schemaImportOptions,
	manifest semantic.Manifest,
	manifestRoot string,
	destination string,
	importedLayer int,
) (schemaImportCanonicalPlan, error) {
	if manifest.CanonicalLayer != importedLayer {
		return schemaImportCanonicalPlan{}, nil
	}

	canonicalPath := options.CanonicalOutput
	if canonicalPath == "" {
		canonicalPath = filepath.Clean(filepath.Join(manifestRoot, "..", "telegram.tl"))
	}
	type protectedPath struct {
		label string
		path  string
	}
	protected := []protectedPath{
		{"schema manifest", options.ManifestPath},
		{"imported layer", destination},
		{"upstream source", options.SourcePath},
		{"default layer policy", filepath.Join(manifestRoot, "policy.json")},
	}
	if policyPath := strings.TrimSpace(options.PolicyPath); policyPath != "" {
		protected = append(protected, protectedPath{"layer policy", policyPath})
	}
	for _, layer := range manifest.Layers {
		protected = append(protected, protectedPath{
			label: fmt.Sprintf("locked Layer %d schema", layer.Layer),
			path:  filepath.Join(manifestRoot, filepath.FromSlash(layer.File)),
		})
	}
	for _, overlay := range manifest.Overlays {
		protected = append(protected, protectedPath{
			label: fmt.Sprintf("locked schema overlay %q", overlay.File),
			path:  filepath.Join(manifestRoot, filepath.FromSlash(overlay.File)),
		})
	}
	for _, candidate := range protected {
		if candidate.path != "" && sameWorkflowPath(canonicalPath, candidate.path) {
			return schemaImportCanonicalPlan{}, fmt.Errorf("canonical schema output must not overwrite %s %q", candidate.label, candidate.path)
		}
	}
	return schemaImportCanonicalPlan{Rebuild: true, Path: canonicalPath}, nil
}

type schemaImportTransactionFile struct {
	Label string
	Path  string
	Data  []byte
	Perm  os.FileMode
}

type stagedSchemaImportFile struct {
	file         schemaImportTransactionFile
	replacement  string
	rollback     string
	previousData []byte
	previousPerm os.FileMode
	existed      bool
}

type schemaImportReplace func(source, destination string) error

// commitSchemaImportTransaction commits all data files before manifestMarker.
// Keeping the marker separate makes it impossible for a caller to accidentally
// place the manifest in the middle of the replacement order.
func commitSchemaImportTransaction(dataFiles []schemaImportTransactionFile, manifestMarker schemaImportTransactionFile) error {
	return commitSchemaImportTransactionWithReplace(dataFiles, manifestMarker, os.Rename)
}

func commitSchemaImportTransactionWithReplace(
	dataFiles []schemaImportTransactionFile,
	manifestMarker schemaImportTransactionFile,
	replace schemaImportReplace,
) error {
	if replace == nil {
		return errors.New("schema import transaction replace function is nil")
	}
	files := make([]schemaImportTransactionFile, 0, len(dataFiles)+1)
	files = append(files, dataFiles...)
	files = append(files, manifestMarker)
	for i, file := range files {
		if file.Path == "" {
			return fmt.Errorf("%s path is empty", file.Label)
		}
		for j := 0; j < i; j++ {
			if sameWorkflowPath(file.Path, files[j].Path) {
				return fmt.Errorf("schema import transaction targets overlap: %s %q and %s %q", files[j].Label, files[j].Path, file.Label, file.Path)
			}
		}
	}

	staged := make([]stagedSchemaImportFile, 0, len(files))
	defer func() {
		for i := range staged {
			removeSchemaImportTemporary(staged[i].replacement)
			removeSchemaImportTemporary(staged[i].rollback)
		}
	}()
	for _, file := range files {
		entry, err := stageSchemaImportFile(file)
		if err != nil {
			return fmt.Errorf("stage %s: %w", file.Label, err)
		}
		staged = append(staged, entry)
	}

	for i := range staged {
		entry := &staged[i]
		if err := replace(entry.replacement, entry.file.Path); err != nil {
			commitErr := fmt.Errorf("replace %s: %w", entry.file.Label, err)
			// Later entries have not been touched. Include the failed entry because
			// a platform or injected implementation may report an error after the
			// destination became visible.
			if rollbackErr := rollbackSchemaImportTransaction(staged[:i+1]); rollbackErr != nil {
				return errors.Join(commitErr, fmt.Errorf("rollback schema import transaction: %w", rollbackErr))
			}
			return commitErr
		}
		entry.replacement = ""
	}
	return nil
}

func stageSchemaImportFile(file schemaImportTransactionFile) (stagedSchemaImportFile, error) {
	entry := stagedSchemaImportFile{file: file}
	replacement, err := stageSchemaImportBytes(file.Path, file.Data, file.Perm, "new")
	if err != nil {
		return stagedSchemaImportFile{}, err
	}
	entry.replacement = replacement
	cleanup := true
	defer func() {
		if cleanup {
			removeSchemaImportTemporary(entry.replacement)
			removeSchemaImportTemporary(entry.rollback)
		}
	}()

	info, err := os.Lstat(file.Path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return stagedSchemaImportFile{}, fmt.Errorf("existing target %q is not a regular file", file.Path)
		}
		entry.previousData, err = os.ReadFile(file.Path)
		if err != nil {
			return stagedSchemaImportFile{}, fmt.Errorf("read existing target: %w", err)
		}
		entry.previousPerm = info.Mode().Perm()
		entry.rollback, err = stageSchemaImportBytes(file.Path, entry.previousData, entry.previousPerm, "old")
		if err != nil {
			return stagedSchemaImportFile{}, fmt.Errorf("stage existing target: %w", err)
		}
		entry.existed = true
	case os.IsNotExist(err):
		// A newly imported Layer commonly has no previous target. Rollback removes
		// it if a later replacement fails.
	default:
		return stagedSchemaImportFile{}, fmt.Errorf("inspect existing target: %w", err)
	}

	cleanup = false
	return entry, nil
}

func stageSchemaImportBytes(target string, data []byte, perm os.FileMode, kind string) (name string, err error) {
	temporary, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".schema-import-"+kind+"-*")
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}
	name = temporary.Name()
	removeTemporary := true
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if removeTemporary {
			removeSchemaImportTemporary(name)
		}
	}()

	if err := temporary.Chmod(perm); err != nil {
		return "", fmt.Errorf("set temporary file mode: %w", err)
	}
	written, err := temporary.Write(data)
	if err != nil {
		return "", fmt.Errorf("write temporary file: %w", err)
	}
	if written != len(data) {
		return "", fmt.Errorf("write temporary file: %w", io.ErrShortWrite)
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close temporary file: %w", err)
	}
	temporary = nil
	removeTemporary = false
	return name, nil
}

func rollbackSchemaImportTransaction(staged []stagedSchemaImportFile) error {
	var rollbackErrors []error
	// Restore the manifest marker first. If a replacement implementation ever
	// reports an error after changing its destination, readers see the old
	// marker while the remaining data files are restored.
	for i := len(staged) - 1; i >= 0; i-- {
		entry := &staged[i]
		if !entry.existed {
			if err := os.Remove(entry.file.Path); err != nil && !os.IsNotExist(err) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new %s: %w", entry.file.Label, err))
			}
			continue
		}
		if schemaImportTargetMatchesPrevious(*entry) {
			continue
		}
		if err := os.Rename(entry.rollback, entry.file.Path); err == nil {
			entry.rollback = ""
			continue
		}
		// A direct restore may be rejected by platform-specific rename rules.
		// Retrying through the existing atomic writer still uses the fully staged
		// previous bytes and never exposes a partially written target.
		if err := writeFileAtomic(entry.file.Path, entry.previousData, entry.previousPerm); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s: %w", entry.file.Label, err))
			continue
		}
		removeSchemaImportTemporary(entry.rollback)
		entry.rollback = ""
	}
	return errors.Join(rollbackErrors...)
}

func schemaImportTargetMatchesPrevious(entry stagedSchemaImportFile) bool {
	info, err := os.Stat(entry.file.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != entry.previousPerm {
		return false
	}
	data, err := os.ReadFile(entry.file.Path)
	return err == nil && bytes.Equal(data, entry.previousData)
}

func removeSchemaImportTemporary(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func sameWorkflowPath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	// The workflow primarily targets Windows, where path casing is not an
	// identity boundary. The conservative comparison is harmless elsewhere.
	return strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

type verifiedSchemaSource struct {
	Commit string
	Blob   string
	Source []byte
}

func loadVerifiedSchemaSource(sourcePath, gitDir, manifestRepository, manifestSourcePath, explicitCommit string) (verifiedSchemaSource, error) {
	if explicitCommit != "" {
		if err := validateObjectID("schema import commit", explicitCommit); err != nil {
			return verifiedSchemaSource{}, err
		}
	}

	var root string
	if gitDir != "" {
		var err error
		root, err = runGit(gitDir, "rev-parse", "--show-toplevel")
		if err != nil {
			return verifiedSchemaSource{}, fmt.Errorf("open schema provenance repository: %w", err)
		}
	} else {
		var relative string
		var err error
		root, relative, err = schemaGitLocation(sourcePath)
		if err != nil {
			return verifiedSchemaSource{}, fmt.Errorf("discover schema provenance: %w; provide -schema-import-git-dir for a copied source", err)
		}
		if got := filepath.ToSlash(relative); got != manifestSourcePath {
			return verifiedSchemaSource{}, fmt.Errorf("discover schema provenance: tracked path %q does not match manifest source_path %q; provide -schema-import-git-dir", got, manifestSourcePath)
		}
	}
	upstreamRemotes, err := matchingSchemaRemotes(root, manifestRepository)
	if err != nil {
		return verifiedSchemaSource{}, err
	}
	commit := explicitCommit
	if commit == "" {
		var err error
		// Lock the commit which introduced the current path contents rather than
		// an unrelated checkout HEAD. This keeps repeated imports stable while
		// upstream advances in other files.
		commit, err = runGit(root, "log", "-1", "--format=%H", "HEAD", "--", manifestSourcePath)
		if err != nil {
			return verifiedSchemaSource{}, fmt.Errorf("discover schema commit: %w", err)
		}
	}
	if err := validateObjectID("discovered schema commit", commit); err != nil {
		return verifiedSchemaSource{}, err
	}
	if err := verifySchemaCommitReachable(root, commit, upstreamRemotes); err != nil {
		return verifiedSchemaSource{}, err
	}
	locked, err := runGit(root, "rev-parse", commit+":"+manifestSourcePath)
	if err != nil {
		return verifiedSchemaSource{}, fmt.Errorf("verify schema provenance at %s:%s: %w", commit, manifestSourcePath, err)
	}
	if err := validateObjectID("schema Git blob", locked); err != nil {
		return verifiedSchemaSource{}, err
	}
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return verifiedSchemaSource{}, fmt.Errorf("resolve imported schema path: %w", err)
	}
	// --path applies the upstream repository's clean/text attributes, so a
	// Windows CRLF checkout is compared to the immutable LF Git object without
	// changing the checked-out file.
	working, err := runGit(root, "hash-object", "--path="+manifestSourcePath, absSource)
	if err != nil {
		return verifiedSchemaSource{}, fmt.Errorf("hash imported schema through Git attributes: %w", err)
	}
	if working != locked {
		return verifiedSchemaSource{}, fmt.Errorf("schema source is dirty or mismatched: filtered local blob %s, %s:%s blob %s", working, commit, manifestSourcePath, locked)
	}
	source, err := runGitBytes(root, "cat-file", "blob", locked)
	if err != nil {
		return verifiedSchemaSource{}, fmt.Errorf("read immutable schema Git blob: %w", err)
	}
	return verifiedSchemaSource{Commit: commit, Blob: locked, Source: source}, nil
}

func matchingSchemaRemotes(root, expected string) ([]string, error) {
	remotes, err := runGit(root, "remote")
	if err != nil {
		return nil, fmt.Errorf("list schema repository remotes: %w", err)
	}
	want := normalizeGitRepository(expected)
	var matched []string
	for _, remote := range strings.Fields(remotes) {
		url, err := runGit(root, "remote", "get-url", remote)
		if err != nil {
			continue
		}
		if normalizeGitRepository(url) == want {
			matched = append(matched, remote)
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("schema provenance repository %q is not configured in local checkout", expected)
	}
	return matched, nil
}

// verifySchemaCommitReachable requires the locked commit to be contained by a
// fetched remote-tracking ref belonging to the manifest's configured upstream.
// Merely adding a matching remote URL is insufficient: otherwise an unrelated
// local-only commit could be recorded as upstream provenance. This remains an
// offline workflow; callers must fetch the upstream checkout before import.
func verifySchemaCommitReachable(root, commit string, remotes []string) error {
	for _, remote := range remotes {
		prefix := "refs/remotes/" + remote
		refs, err := runGit(root, "for-each-ref", "--format=%(refname)", "--contains="+commit, prefix)
		if err != nil {
			return fmt.Errorf("verify schema commit against remote %q: %w", remote, err)
		}
		for _, ref := range strings.Fields(refs) {
			if strings.HasPrefix(ref, prefix+"/") && ref != prefix+"/HEAD" {
				return nil
			}
		}
	}
	return fmt.Errorf(
		"schema provenance commit %s is not reachable from any fetched ref of configured upstream remote(s) %s; fetch upstream before import",
		commit, strings.Join(remotes, ","),
	)
}

func normalizeGitRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	if strings.HasPrefix(repository, "git@github.com:") {
		repository = "https://github.com/" + strings.TrimPrefix(repository, "git@github.com:")
	}
	if strings.HasPrefix(repository, "ssh://git@github.com/") {
		repository = "https://github.com/" + strings.TrimPrefix(repository, "ssh://git@github.com/")
	}
	return strings.TrimSuffix(strings.TrimSuffix(repository, "/"), ".git")
}

func schemaGitLocation(sourcePath string) (root, relative string, err error) {
	abs, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", "", err
	}
	sourceDir := filepath.Dir(abs)
	root, err = runGit(sourceDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	// Ask Git for the repository-relative directory instead of combining
	// filepath.Abs and Git's canonical root. Those paths can name the same
	// directory differently: macOS resolves /var to /private/var, while Windows
	// runners may mix long paths with their 8.3 aliases. Git already owns the
	// tracked-path identity needed by the provenance check.
	prefix, err := runGit(sourceDir, "rev-parse", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	relative = filepath.Clean(filepath.Join(filepath.FromSlash(prefix), filepath.Base(abs)))
	return root, relative, nil
}

func runGit(dir string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", commandArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(output))
	}
	return strings.TrimSpace(string(output)), nil
}

func runGitBytes(dir string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", commandArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return output, nil
}

func validateObjectID(name, value string) error {
	if value != strings.ToLower(value) {
		return fmt.Errorf("%s must be lowercase hex", name)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 20 {
		return fmt.Errorf("%s must be a 20-byte hex object ID", name)
	}
	return nil
}

func runLayerPolicyAudit(manifestPath, policyPath, auditPath, mergePath string) (gen.LayerPolicyAuditDocument, error) {
	set, err := semantic.LoadUniverse(manifestPath)
	if err != nil {
		return gen.LayerPolicyAuditDocument{}, err
	}
	policy, err := gen.LoadLayerPolicy(policyPath)
	if err != nil {
		return gen.LayerPolicyAuditDocument{}, err
	}
	report, err := gen.AnalyzeLayerObligations(set, gen.LayerObligationPolicy{})
	if err != nil {
		return gen.LayerPolicyAuditDocument{}, err
	}
	audit, err := gen.AuditLayerPolicy(report, policy)
	if err != nil {
		return gen.LayerPolicyAuditDocument{}, err
	}
	if auditPath != "" {
		data, err := gen.MarshalLayerPolicyAudit(audit)
		if err != nil {
			return gen.LayerPolicyAuditDocument{}, err
		}
		if err := writeWorkflowOutput(auditPath, data); err != nil {
			return gen.LayerPolicyAuditDocument{}, fmt.Errorf("write layer policy audit: %w", err)
		}
	}
	if mergePath != "" {
		data, err := gen.MarshalLayerPolicyDocument(audit.MergedPolicy())
		if err != nil {
			return gen.LayerPolicyAuditDocument{}, err
		}
		if err := writeWorkflowOutput(mergePath, data); err != nil {
			return gen.LayerPolicyAuditDocument{}, fmt.Errorf("write merged layer policy: %w", err)
		}
	}
	return audit, nil
}

func writeWorkflowOutput(path string, data []byte) error {
	if path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	return writeFileAtomic(path, data, 0o644)
}
