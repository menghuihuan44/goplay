package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Version identifies the report schema.
const Version = "audit-v1"

// Options controls one audit run.
type Options struct {
	Root         string
	Allow        []string
	SupportDirs  []string
	Rules        []Rule
	MaxFileBytes int64
	SkipPE       bool
	Timeout      time.Duration
}

// DefaultOptions returns the repository defaults: the audit's own rule tables
// and generated reports are allowlisted, and test fixture directories are
// treated as support data.
func DefaultOptions(root string) Options {
	return Options{
		Root:         root,
		Allow:        []string{"internal/audit/**", "reports/**", "**/testdata/**"},
		SupportDirs:  []string{"testdata", "internal/audit/testdata"},
		MaxFileBytes: 8 << 20,
		Timeout:      2 * time.Minute,
	}
}

// Stats summarizes what the audit inspected.
type Stats struct {
	Walked          int   `json:"walked"`
	SourceFiles     int   `json:"source_files"`
	SourceBytes     int64 `json:"source_bytes"`
	Allowlisted     int   `json:"allowlisted"`
	SupportSkipped  int   `json:"support_skipped"`
	OversizeSkipped int   `json:"oversize_skipped"`
	NativeFiles     int   `json:"native_files"`
	NativeSymbols   int   `json:"native_imported_symbols"`
	NativeInvalid   int   `json:"native_invalid"`
	PECritical      int   `json:"pe_critical_imports"`
	PEReview        int   `json:"pe_review_imports"`
}

// Report is the audit result, suitable for JSON or human-readable output.
type Report struct {
	Version        string     `json:"version"`
	Root           string     `json:"root"`
	GeneratedAt    time.Time  `json:"generated_at"`
	DurationMS     int64      `json:"duration_ms"`
	Verdict        string     `json:"verdict"`
	SourceFindings []Finding  `json:"source_findings"`
	PEImports      []PEImport `json:"pe_imports"`
	PEInvalid      []PEIssue  `json:"pe_invalid"`
	Stats          Stats      `json:"stats"`
	Allowlist      []string   `json:"allowlist"`
	SupportDirs    []string   `json:"support_dirs"`
	Warnings       []string   `json:"warnings,omitempty"`
}

// Run scans source files and native PE import tables under opts.Root.
// It never executes or loads the files it inspects.
func Run(ctx context.Context, opts Options) (*Report, error) {
	start := time.Now()
	def := DefaultOptions(opts.Root)
	if opts.Root == "" {
		opts.Root = def.Root
	}
	if opts.MaxFileBytes == 0 {
		opts.MaxFileBytes = def.MaxFileBytes
	}
	if opts.Rules == nil {
		opts.Rules = DefaultRules()
	}
	if opts.Allow == nil {
		opts.Allow = def.Allow
	}
	if opts.SupportDirs == nil {
		opts.SupportDirs = def.SupportDirs
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("audit: resolve root: %w", err)
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("audit: root %s: %w", root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("audit: root %s is not a directory", root)
	}
	rep := &Report{
		Version:     Version,
		Root:        root,
		GeneratedAt: time.Now(),
		Allowlist:   append([]string(nil), opts.Allow...),
		SupportDirs: append([]string(nil), opts.SupportDirs...),
	}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			rel, _ := filepath.Rel(root, path)
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: %v", filepath.ToSlash(rel), err))
			return nil
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		rep.Stats.Walked++
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if underAny(rel, opts.SupportDirs) {
				rep.Stats.SupportSkipped++
				return filepath.SkipDir
			}
			if matchAllow(rel+"/", opts.Allow) {
				rep.Stats.Allowlisted++
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if underAny(rel, opts.SupportDirs) {
			rep.Stats.SupportSkipped++
			return nil
		}
		if matchAllow(rel, opts.Allow) {
			rep.Stats.Allowlisted++
			return nil
		}
		if SourceExtensions[strings.ToLower(filepath.Ext(rel))] {
			content, over, rerr := readSource(path, opts.MaxFileBytes)
			if over {
				rep.Stats.OversizeSkipped++
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: %v", filepath.ToSlash(rel), rerr))
				return nil
			}
			if rerr != nil {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: %v", filepath.ToSlash(rel), rerr))
				return nil
			}
			rep.Stats.SourceFiles++
			rep.Stats.SourceBytes += int64(len(content))
			rep.SourceFindings = append(rep.SourceFindings, scanSourceFile(rel, string(content), opts.Rules)...)
			return nil
		}
		if !opts.SkipPE && isNativeArtifact(rel) {
			rep.Stats.NativeFiles++
			imports, nsyms, ierr := inspectPE(rel, path)
			if ierr != nil {
				rep.Stats.NativeInvalid++
				rep.PEInvalid = append(rep.PEInvalid, PEIssue{Path: filepath.ToSlash(rel), Error: ierr.Error()})
				return nil
			}
			rep.Stats.NativeSymbols += nsyms
			rep.PEImports = append(rep.PEImports, imports...)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("audit: walk %s: %w", root, walkErr)
	}
	sortFindings(rep.SourceFindings)
	sort.SliceStable(rep.PEImports, func(i, j int) bool {
		if rep.PEImports[i].Path != rep.PEImports[j].Path {
			return rep.PEImports[i].Path < rep.PEImports[j].Path
		}
		return rep.PEImports[i].Symbol < rep.PEImports[j].Symbol
	})
	sort.SliceStable(rep.PEInvalid, func(i, j int) bool { return rep.PEInvalid[i].Path < rep.PEInvalid[j].Path })
	for _, imp := range rep.PEImports {
		if imp.Severity == "critical" {
			rep.Stats.PECritical++
		} else {
			rep.Stats.PEReview++
		}
	}
	if rep.Stats.PEReview > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"%d review-level PE imports (OpenProcess/SetThreadContext/GetThreadContext): common in the MSVC C runtime and Go runtime; confirm no source uses them", rep.Stats.PEReview))
	}
	if len(rep.SourceFindings) == 0 && rep.Stats.PECritical == 0 {
		rep.Verdict = "PASS"
	} else {
		rep.Verdict = "FAIL"
	}
	rep.DurationMS = time.Since(start).Milliseconds()
	return rep, nil
}

// JSON renders the report as indented JSON.
func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// WriteJSON writes the JSON report to path.
func (r *Report) WriteJSON(path string) error {
	b, err := r.JSON()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// WriteText writes the human-readable report to path.
func (r *Report) WriteText(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(r.Human()), 0o644)
}

// Human renders a compact, human-readable audit report.
func (r *Report) Human() string {
	var b strings.Builder
	fmt.Fprintf(&b, "goplay safety static audit (%s)\n", r.Version)
	fmt.Fprintf(&b, "root:    %s\n", r.Root)
	fmt.Fprintf(&b, "verdict: %s\n", r.Verdict)
	fmt.Fprintf(&b, "elapsed: %d ms\n", r.DurationMS)
	fmt.Fprintf(&b, "scanned: %d source files (%d bytes), %d native PE files (%d imported symbols)\n",
		r.Stats.SourceFiles, r.Stats.SourceBytes, r.Stats.NativeFiles, r.Stats.NativeSymbols)
	fmt.Fprintf(&b, "skipped: %d allowlisted, %d support dirs, %d oversize; %d invalid native files\n\n",
		r.Stats.Allowlisted, r.Stats.SupportSkipped, r.Stats.OversizeSkipped, r.Stats.NativeInvalid)

	fmt.Fprintf(&b, "Source findings (%d)\n", len(r.SourceFindings))
	if len(r.SourceFindings) == 0 {
		fmt.Fprintf(&b, "  none: no forbidden injection or memory-tampering symbols found\n")
	}
	for _, f := range r.SourceFindings {
		fmt.Fprintf(&b, "  - [%s] %s  %s:%d:%d\n      %s\n", f.Severity, f.Symbol, f.Path, f.Line, f.Column, f.Snippet)
	}
	fmt.Fprintf(&b, "\nPE import findings (critical: %d, review: %d)\n", r.Stats.PECritical, r.Stats.PEReview)
	if len(r.PEImports) == 0 {
		fmt.Fprintf(&b, "  none: no forbidden symbols in parsed PE import tables\n")
	}
	for _, p := range r.PEImports {
		lib := p.Library
		if lib == "" {
			lib = "?"
		}
		fmt.Fprintf(&b, "  - [%s] %s imports %s!%s\n", p.Severity, p.Path, lib, p.Symbol)
	}
	if len(r.PEInvalid) > 0 {
		fmt.Fprintf(&b, "\nNative files not parsed (%d)\n", len(r.PEInvalid))
		for _, p := range r.PEInvalid {
			fmt.Fprintf(&b, "  - %s: %s\n", p.Path, p.Error)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintf(&b, "\nWarnings (%d)\n", len(r.Warnings))
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "  - %s\n", w)
		}
	}
	fmt.Fprintf(&b, "\nAllowlist: %s\n", strings.Join(r.Allowlist, ", "))
	fmt.Fprintf(&b, "Support dirs: %s\n", strings.Join(r.SupportDirs, ", "))
	fmt.Fprintf(&b, "\nThe audit parses files without executing or mapping them; it does not inject into or hook any process.\n")
	return b.String()
}
