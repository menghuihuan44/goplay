// Package eval builds repeatable, end-to-end evaluations for goplay using the
// mock dungeon and the real perception, policy, scheduler, memory and engine
// packages. It produces machine-readable JSON and a human-readable Markdown
// report with explicit pass/fail thresholds.
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Version is the report schema version. Bump it when field semantics change.
const Version = "eval-v1"

// Metric is one measured value. A metric with Threshold and Op set is a
// pass/fail check; without them it is informational context.
type Metric struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Value     float64  `json:"value"`
	Unit      string   `json:"unit,omitempty"`
	Threshold *float64 `json:"threshold,omitempty"`
	Op        string   `json:"op,omitempty"`
	Pass      *bool    `json:"pass,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// Suite is one coherent evaluation area.
type Suite struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Status     string         `json:"status"` // pass, fail, error
	DurationMS int64          `json:"duration_ms"`
	Metrics    []Metric       `json:"metrics"`
	Details    map[string]any `json:"details,omitempty"`
	Notes      []string       `json:"notes,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// Check is a flattened threshold row used by the threshold table.
type Check struct {
	ID        string  `json:"id"`
	Suite     string  `json:"suite"`
	Label     string  `json:"label"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit,omitempty"`
	Op        string  `json:"op"`
	Threshold float64 `json:"threshold"`
	Pass      bool    `json:"pass"`
	Note      string  `json:"note,omitempty"`
}

// Environment records the host facts that affect reproducibility.
type Environment struct {
	GoVersion  string `json:"go_version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	NumCPU     int    `json:"num_cpu"`
	WorkingDir string `json:"working_dir,omitempty"`
}

// Summary is the top-level verdict.
type Summary struct {
	Suites       int    `json:"suites"`
	SuitesPassed int    `json:"suites_passed"`
	SuitesFailed int    `json:"suites_failed"`
	ChecksTotal  int    `json:"checks_total"`
	ChecksPassed int    `json:"checks_passed"`
	ChecksFailed int    `json:"checks_failed"`
	Verdict      string `json:"verdict"` // PASS or FAIL
	DurationMS   int64  `json:"duration_ms"`
}

// Report is the full evaluation artifact.
type Report struct {
	Version      string      `json:"version"`
	Mode         string      `json:"mode"`
	GeneratedAt  time.Time   `json:"generated_at"`
	DurationMS   int64       `json:"duration_ms"`
	Environment  Environment `json:"environment"`
	Summary      Summary     `json:"summary"`
	Checks       []Check     `json:"checks"`
	Suites       []Suite     `json:"suites"`
	Limitations  []string    `json:"limitations"`
	JSONPath     string      `json:"json_path,omitempty"`
	MarkdownPath string      `json:"markdown_path,omitempty"`
}

// Add records an informational metric.
func (s *Suite) Add(id, label string, value float64, unit, note string) {
	s.Metrics = append(s.Metrics, Metric{ID: id, Label: label, Value: value, Unit: unit, Note: note})
}

// Check records a threshold metric and immediately evaluates it.
func (s *Suite) Check(id, label string, value float64, op string, threshold float64, unit, note string) bool {
	pass := compare(value, op, threshold)
	th := threshold
	s.Metrics = append(s.Metrics, Metric{
		ID: id, Label: label, Value: value, Unit: unit,
		Threshold: &th, Op: op, Pass: &pass, Note: note,
	})
	return pass
}

func compare(v float64, op string, threshold float64) bool {
	switch op {
	case ">=":
		return v >= threshold
	case "<=":
		return v <= threshold
	case "==":
		return v == threshold
	case ">":
		return v > threshold
	case "<":
		return v < threshold
	default:
		return false
	}
}

// Finalize sets the suite status from its checks. An explicit error wins.
func (s *Suite) Finalize() {
	if s.Error != "" {
		s.Status = "error"
		return
	}
	s.Status = "pass"
	for _, m := range s.Metrics {
		if m.Pass != nil && !*m.Pass {
			s.Status = "fail"
			return
		}
	}
}

func (r *Report) evaluate() {
	r.Summary = Summary{Suites: len(r.Suites), DurationMS: r.DurationMS}
	r.Checks = r.Checks[:0]
	for _, s := range r.Suites {
		if s.Status == "pass" {
			r.Summary.SuitesPassed++
		} else {
			r.Summary.SuitesFailed++
		}
		for _, m := range s.Metrics {
			if m.Pass == nil || m.Threshold == nil {
				continue
			}
			r.Checks = append(r.Checks, Check{
				ID: m.ID, Suite: s.ID, Label: m.Label, Value: m.Value,
				Unit: m.Unit, Op: m.Op, Threshold: *m.Threshold, Pass: *m.Pass, Note: m.Note,
			})
		}
	}
	r.Summary.ChecksTotal = len(r.Checks)
	for _, c := range r.Checks {
		if c.Pass {
			r.Summary.ChecksPassed++
		} else {
			r.Summary.ChecksFailed++
		}
	}
	if r.Summary.ChecksFailed == 0 && r.Summary.SuitesFailed == 0 {
		r.Summary.Verdict = "PASS"
	} else {
		r.Summary.Verdict = "FAIL"
	}
}

// JSON renders the report as indented JSON.
func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Write persists the JSON and Markdown reports into dir and records the paths.
// It returns the two paths.
func (r *Report) Write(dir string) (string, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	r.JSONPath = filepath.Join(dir, "eval-report.json")
	r.MarkdownPath = filepath.Join(dir, "eval-report.md")
	b, err := r.JSON()
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(r.JSONPath, append(b, '\n'), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(r.MarkdownPath, []byte(r.Markdown()), 0o644); err != nil {
		return "", "", err
	}
	return r.JSONPath, r.MarkdownPath, nil
}

func formatValue(v float64, unit string) string {
	switch unit {
	case "ratio":
		return fmt.Sprintf("%.4f", v)
	case "ms":
		return fmt.Sprintf("%.2f ms", v)
	case "count":
		return fmt.Sprintf("%.0f", v)
	case "bytes":
		return fmt.Sprintf("%.0f", v)
	case "tps":
		return fmt.Sprintf("%.1f", v)
	case "tokens":
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%.4g", v)
	}
}

// Markdown renders the human report. It is intentionally plain Markdown so it
// can be pasted into an issue or reviewed in a terminal pager.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# goplay evaluation report\n\n")
	fmt.Fprintf(&b, "- **Schema**: %s\n", r.Version)
	fmt.Fprintf(&b, "- **Mode**: %s\n", r.Mode)
	fmt.Fprintf(&b, "- **Generated**: %s\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Duration**: %d ms\n", r.DurationMS)
	fmt.Fprintf(&b, "- **Environment**: %s/%s, Go %s, %d CPUs\n\n", r.Environment.OS, r.Environment.Arch, r.Environment.GoVersion, r.Environment.NumCPU)
	if r.Summary.Verdict == "PASS" {
		fmt.Fprintf(&b, "## Verdict: PASS\n\n")
	} else {
		fmt.Fprintf(&b, "## Verdict: FAIL\n\n")
	}
	fmt.Fprintf(&b, "suites: %d passed / %d failed; threshold checks: %d passed / %d failed\n\n",
		r.Summary.SuitesPassed, r.Summary.SuitesFailed, r.Summary.ChecksPassed, r.Summary.ChecksFailed)

	fmt.Fprintf(&b, "## Threshold table\n\n")
	fmt.Fprintf(&b, "| # | Suite | Metric | Value | Rule | Pass |\n|---|-------|--------|-------|------|------|\n")
	for i, c := range r.Checks {
		mark := "PASS"
		if !c.Pass {
			mark = "**FAIL**"
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s %s | %s |\n",
			i+1, c.Suite, c.Label, formatValue(c.Value, c.Unit), c.Op, formatValue(c.Threshold, c.Unit), mark)
	}
	if len(r.Checks) == 0 {
		fmt.Fprintf(&b, "| - | - | no threshold checks | - | - | - |\n")
	}
	b.WriteString("\n")

	for _, s := range r.Suites {
		fmt.Fprintf(&b, "## %s (%s, %d ms)\n\n", s.Title, s.Status, s.DurationMS)
		if s.Error != "" {
			fmt.Fprintf(&b, "**Error**: %s\n\n", s.Error)
		}
		fmt.Fprintf(&b, "| Metric | Value | Threshold |\n|--------|-------|-----------|\n")
		for _, m := range s.Metrics {
			val := formatValue(m.Value, m.Unit)
			rule := "-"
			if m.Threshold != nil {
				rule = fmt.Sprintf("%s %s", m.Op, formatValue(*m.Threshold, m.Unit))
				if m.Pass != nil && !*m.Pass {
					rule += " (**FAIL**)"
				}
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", m.Label, val, rule)
		}
		b.WriteString("\n")
		for _, n := range s.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		if len(s.Notes) > 0 {
			b.WriteString("\n")
		}
		if len(s.Details) > 0 {
			// Keep Markdown readable: pretty JSON in a collapsible block.
			db, err := json.MarshalIndent(s.Details, "", "  ")
			if err == nil {
				fmt.Fprintf(&b, "<details><summary>Raw %s details</summary>\n\n```json\n%s\n```\n\n</details>\n\n", s.ID, db)
			}
		}
	}

	fmt.Fprintf(&b, "## Limitations\n\n")
	for _, l := range r.Limitations {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	return b.String()
}

// sortedStrings is a small helper for deterministic detail rendering.
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func envInfo() Environment {
	wd, _ := os.Getwd()
	return Environment{
		GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		NumCPU: runtime.NumCPU(), WorkingDir: wd,
	}
}
