// Binary gotdgen generates go source code from TL schema.
package main

import (
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gotd/tl"

	"github.com/iamxvbaba/td/gen"
	"github.com/iamxvbaba/td/gen/semantic"
)

type formattedSource struct {
	Format bool
	Root   string
	files  map[string][]byte
}

func (t *formattedSource) WriteFile(name string, content []byte) error {
	if _, duplicate := t.files[name]; duplicate {
		return fmt.Errorf("duplicate generated file %q", name)
	}
	out := content
	if t.Format {
		buf, err := format.Source(content)
		if err != nil {
			return err
		}
		out = buf
	}
	t.files[name] = append([]byte(nil), out...)
	return nil
}

// Commit writes only after every template has rendered and formatted
// successfully. Individual targets are atomically replaced in deterministic
// order, so a generation/format error can never leave a mixed package.
func (t *formattedSource) Commit() error {
	names := make([]string, 0, len(t.files))
	for name := range t.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeFileAtomic(filepath.Join(t.Root, name), t.files[name], 0o600); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
}

func (t *formattedSource) Has(name string) bool {
	_, ok := t.files[name]
	return ok
}

func main() {
	schemaPath := flag.String("schema", "", "Path to .tl file")
	schemaManifest := flag.String("schema-manifest", "", "Path to locked multi-layer schema manifest")
	schemaImport := flag.String("schema-import", "", "Import a local layer .tl source into the locked schema manifest and exit")
	schemaImportGitDir := flag.String("schema-import-git-dir", "", "Local upstream Git checkout used to verify commit:path provenance for a copied source")
	schemaImportCommit := flag.String("schema-import-commit", "", "Exact upstream commit for a copied import (auto-discovered for a clean tracked source)")
	schemaImportBlob := flag.String("schema-import-blob", "", "Optional expected Git blob ID for the imported source")
	schemaImportCanonical := flag.Bool("schema-import-canonical", true, "Select the imported layer as canonical and rebuild _schema/telegram.tl")
	schemaImportReplace := flag.Bool("schema-import-replace", false, "Explicitly replace different provenance for an existing layer")
	canonicalSchemaOutput := flag.String("canonical-schema-output", "", "Canonical merged .tl output (default: ../telegram.tl relative to manifest)")
	layerPolicy := flag.String("layer-policy", "", "Path to versioned multi-layer conversion policy")
	layerPolicyTemplate := flag.String("layer-policy-template", "", "Write unresolved multi-layer policy skeleton to path (or - for stdout) and exit")
	layerPolicyAudit := flag.String("layer-policy-audit", "", "Write retained/stale/new policy migration report to path (or - for stdout) and exit")
	layerPolicyMerge := flag.String("layer-policy-merge", "", "Write retained plus unresolved skeleton policy to path (or - for stdout) and exit")
	layerPlanAudit := flag.String("layer-plan-audit", "", "Write deterministic sparse execution-plan audit JSON after successful generation")
	targetDir := flag.String("target", "td", "Path to target dir")
	packageName := flag.String("package", "td", "Target package name")
	layerTargetDir := flag.String("layer-target", "", "Separate target directory for generated tlprofile sidecar")
	layerPackageName := flag.String("layer-package", "", "Package name for generated tlprofile sidecar")
	performFormat := flag.Bool("format", true, "Perform code formatting")
	clean := flag.Bool("clean", false, "Clean generated files before generation")

	genOpts := gen.GeneratorOptions{}
	genOpts.RegisterFlags(flag.CommandLine)

	flag.Parse()
	if (*schemaPath == "") == (*schemaManifest == "") {
		panic("provide exactly one of -schema or -schema-manifest")
	}
	if (*layerTargetDir == "") != (*layerPackageName == "") {
		panic("-layer-target and -layer-package must be provided together")
	}
	if *layerTargetDir != "" && *schemaManifest == "" {
		panic("-layer-target requires -schema-manifest")
	}
	if *schemaImport != "" {
		if *schemaManifest == "" {
			panic("-schema-import requires -schema-manifest")
		}
		if *layerPolicyTemplate != "" || *layerPolicyAudit != "" || *layerPolicyMerge != "" {
			panic("-schema-import is a standalone workflow; run policy audit after import")
		}
		entry, err := runSchemaImport(schemaImportOptions{
			ManifestPath:    *schemaManifest,
			SourcePath:      *schemaImport,
			GitDir:          *schemaImportGitDir,
			Commit:          *schemaImportCommit,
			ExpectedBlob:    *schemaImportBlob,
			Canonical:       *schemaImportCanonical,
			Replace:         *schemaImportReplace,
			CanonicalOutput: *canonicalSchemaOutput,
			PolicyPath:      *layerPolicy,
		})
		if err != nil {
			panic(fmt.Sprintf("import layer schema: %+v", err))
		}
		fmt.Printf("Imported Layer %d (%s, blob %s, sha256 %s)\n", entry.Layer, entry.Commit, entry.Blob, entry.SHA256)
		return
	}
	if *schemaImportGitDir != "" || *schemaImportCommit != "" || *schemaImportBlob != "" || *schemaImportReplace || *canonicalSchemaOutput != "" {
		panic("schema import options require -schema-import")
	}
	if *layerPolicy != "" && *schemaManifest == "" {
		panic("-layer-policy requires -schema-manifest")
	}
	if *layerPlanAudit != "" && *schemaManifest == "" {
		panic("-layer-plan-audit requires -schema-manifest")
	}
	if *layerPolicyTemplate != "" && *schemaManifest == "" {
		panic("-layer-policy-template requires -schema-manifest")
	}
	if *layerPolicyAudit != "" || *layerPolicyMerge != "" {
		if *schemaManifest == "" || *layerPolicy == "" {
			panic("-layer-policy-audit/-layer-policy-merge require -schema-manifest and -layer-policy")
		}
		if *layerPolicyTemplate != "" {
			panic("policy audit/merge cannot be combined with -layer-policy-template")
		}
		if *layerPolicyAudit == "-" && *layerPolicyMerge == "-" {
			panic("policy audit and merged policy cannot both be written to stdout")
		}
		if *layerPolicyAudit != "" && *layerPolicyMerge != "" && *layerPolicyAudit != "-" && *layerPolicyMerge != "-" && filepath.Clean(*layerPolicyAudit) == filepath.Clean(*layerPolicyMerge) {
			panic("policy audit and merged policy require different output paths")
		}
		audit, err := runLayerPolicyAudit(*schemaManifest, *layerPolicy, *layerPolicyAudit, *layerPolicyMerge)
		if err != nil {
			panic(fmt.Sprintf("audit layer policy: %+v", err))
		}
		summary := os.Stdout
		if *layerPolicyAudit == "-" || *layerPolicyMerge == "-" {
			summary = os.Stderr
		}
		fmt.Fprintf(summary, "Layer policy audit complete: retained=%d stale=%d new=%d\n", len(audit.Retained), len(audit.Stale), len(audit.New))
		return
	}

	start := time.Now()
	var g *gen.Generator
	if *schemaManifest != "" {
		schemaSet, err := semantic.LoadUniverse(*schemaManifest)
		if err != nil {
			panic(fmt.Sprintf("load schema manifest: %+v", err))
		}
		if *layerPolicy != "" {
			policy, err := gen.LoadLayerPolicy(*layerPolicy)
			if err != nil {
				panic(fmt.Sprintf("load layer policy: %+v", err))
			}
			genOpts.LayerPolicy = policy
		}
		g, err = gen.NewSchemaSetGenerator(schemaSet, genOpts)
		if err != nil {
			panic(fmt.Sprintf("build schema set generator: %+v", err))
		}
	} else {
		f, err := os.Open(*schemaPath)
		if err != nil {
			panic(err)
		}
		defer func() { _ = f.Close() }()

		schema, err := tl.Parse(f)
		if err != nil {
			panic(err)
		}
		g, err = gen.NewGenerator(schema, genOpts)
		if err != nil {
			panic(fmt.Sprintf("%+v", err))
		}
	}
	collectInfoTime := time.Since(start)
	if *layerPolicyTemplate != "" {
		plan := g.LayerConversionPlan()
		if plan == nil {
			panic("layer policy template requires a schema-set conversion plan")
		}
		data, err := gen.MarshalLayerPolicyTemplate(plan.Report)
		if err != nil {
			panic(fmt.Sprintf("render layer policy template: %+v", err))
		}
		if *layerPolicyTemplate == "-" {
			if _, err := os.Stdout.Write(data); err != nil {
				panic(fmt.Sprintf("write layer policy template: %+v", err))
			}
			return
		}
		if err := writeFileAtomic(*layerPolicyTemplate, data, 0o600); err != nil {
			panic(fmt.Sprintf("write layer policy template: %+v", err))
		}
		fmt.Printf("Layer policy template written to %s (%d unresolved decisions)\n", *layerPolicyTemplate, len(plan.Report.Unresolved()))
		return
	}

	files, err := os.ReadDir(*targetDir)
	if err != nil && !os.IsNotExist(err) {
		panic(err)
	}
	if os.IsNotExist(err) {
		if err := os.Mkdir(*targetDir, 0750); err != nil {
			panic(err)
		}
	}
	fs := &formattedSource{
		Root:   *targetDir,
		Format: *performFormat,
		files:  make(map[string][]byte),
	}
	var layerFS *formattedSource
	var layerFiles []os.DirEntry
	if *layerTargetDir != "" {
		layerFiles, err = os.ReadDir(*layerTargetDir)
		if err != nil && !os.IsNotExist(err) {
			panic(err)
		}
		if os.IsNotExist(err) {
			if err := os.MkdirAll(*layerTargetDir, 0o750); err != nil {
				panic(err)
			}
		}
		layerFS = &formattedSource{
			Root:   *layerTargetDir,
			Format: *performFormat,
			files:  make(map[string][]byte),
		}
	}
	start = time.Now()
	if err := g.WriteSource(fs, *packageName, gen.Template()); err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	if layerFS != nil {
		if err := g.WriteTLProfileSource(layerFS, *layerPackageName, gen.Template()); err != nil {
			panic(fmt.Sprintf("%+v", err))
		}
	}
	if err := fs.Commit(); err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	if layerFS != nil {
		if err := layerFS.Commit(); err != nil {
			panic(fmt.Sprintf("%+v", err))
		}
	}
	if *layerPlanAudit != "" {
		data, err := g.MarshalLayerExecutionAudit()
		if err != nil {
			panic(fmt.Sprintf("build layer plan audit: %+v", err))
		}
		if err := os.MkdirAll(filepath.Dir(*layerPlanAudit), 0o750); err != nil {
			panic(fmt.Sprintf("create layer plan audit directory: %+v", err))
		}
		if err := writeFileAtomic(*layerPlanAudit, data, 0o600); err != nil {
			panic(fmt.Sprintf("write layer plan audit: %+v", err))
		}
	}
	if *clean {
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			if !strings.HasSuffix(name, "_gen.go") || !strings.HasPrefix(name, "tl_") || fs.Has(name) {
				continue
			}
			if err := os.Remove(filepath.Join(*targetDir, name)); err != nil {
				panic(err)
			}
		}
		if layerFS != nil {
			for _, f := range layerFiles {
				if f.IsDir() {
					continue
				}
				name := f.Name()
				if !strings.HasSuffix(name, "_gen.go") || !strings.HasPrefix(name, "tl_") || layerFS.Has(name) {
					continue
				}
				if err := os.Remove(filepath.Join(*layerTargetDir, name)); err != nil {
					panic(err)
				}
			}
		}
	}
	writeTime := time.Since(start)

	fmt.Printf("Generation %s complete, collect time: %s, write time: %s\n",
		*packageName,
		collectInfoTime,
		writeTime,
	)
}
