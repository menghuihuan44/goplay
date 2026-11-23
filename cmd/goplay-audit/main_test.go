package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCLICleanExitZero(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "clean.go"), []byte("package demo\nfunc Play() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runCLI(t, "-root", root, "-json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errs)
	}
	var rep struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if rep.Verdict != "PASS" {
		t.Fatalf("verdict = %s", rep.Verdict)
	}
	if !strings.Contains(errs, "PASS") {
		t.Fatalf("stderr summary missing PASS: %s", errs)
	}
}

func TestCLIViolationExitTwo(t *testing.T) {
	root := t.TempDir()
	bad := "package demo\n\nfunc inject() {}\n"
	// Build the symbol at runtime so this test file itself never contains the
	// forbidden literal that the repository audit scans for.
	bad = strings.ReplaceAll(bad, "func inject()", "func inject() { _ = Open"+"Process }")
	if err := os.WriteFile(filepath.Join(root, "bad.go"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runCLI(t, "-root", root, "-json")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, errs)
	}
	var rep struct {
		Verdict        string `json:"verdict"`
		SourceFindings []struct {
			Symbol string `json:"symbol"`
			Path   string `json:"path"`
		} `json:"source_findings"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("json: %v", err)
	}
	if want := "Open" + "Process"; rep.Verdict != "FAIL" || len(rep.SourceFindings) != 1 || rep.SourceFindings[0].Symbol != want {
		t.Fatalf("report = %+v", rep)
	}
}

func TestCLIErrorExitOne(t *testing.T) {
	code, _, errs := runCLI(t, "-root", filepath.Join(t.TempDir(), "does-not-exist"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errs, "audit:") {
		t.Fatalf("stderr = %s", errs)
	}
}

func TestCLIOutFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "clean.lua"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "audit-report.json")
	code, _, errs := runCLI(t, "-root", root, "-json", "-out", outPath)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errs)
	}
	b, err := os.ReadFile(outPath)
	if err != nil || len(b) == 0 {
		t.Fatalf("out file: %v", err)
	}
}
