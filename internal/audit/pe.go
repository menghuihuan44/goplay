package audit

import (
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PEImport is one forbidden symbol imported by a native artifact.
type PEImport struct {
	Path     string `json:"path"`
	Library  string `json:"library"`
	Symbol   string `json:"symbol"`
	Severity string `json:"severity"`
}

// PEIssue records a native artifact that could not be parsed as a PE image.
type PEIssue struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// criticalImportSymbols are imports that are almost impossible to justify in
// an external play assistant: they read or write another process, create a
// thread there, or install a global hook. Finding one fails the audit.
var criticalImportSymbols = map[string]string{
	"readprocessmemory":    "ReadProcessMemory",
	"writeprocessmemory":   "WriteProcessMemory",
	"createremotethread":   "CreateRemoteThread",
	"createremotethreadex": "CreateRemoteThreadEx",
	"virtualprotectex":     "VirtualProtectEx",
	"virtualallocex":       "VirtualAllocEx",
	"ntreadvirtualmemory":  "NtReadVirtualMemory",
	"ntwritevirtualmemory": "NtWriteVirtualMemory",
	"ntcreatethreadex":     "NtCreateThreadEx",
	"ntmapviewsection":     "NtMapViewOfSection",
	"queueuserapc":         "QueueUserAPC",
	"ldrloaddll":           "LdrLoadDll",
	"setwindowshookex":     "SetWindowsHookEx",
	"setwindowshookexa":    "SetWindowsHookExA",
	"setwindowshookexw":    "SetWindowsHookExW",
}

// reviewImportSymbols are suspicious but not decisive at the import-table
// level. MSVC's C runtime and the Go runtime import OpenProcess and
// SetThreadContext for their own bookkeeping, so these are reported for human
// review without failing the build. Source scanning still rejects actual uses.
var reviewImportSymbols = map[string]string{
	"openprocess":      "OpenProcess",
	"setthreadcontext": "SetThreadContext",
	"getthreadcontext": "GetThreadContext",
}

// inspectPE parses only the PE headers and import table with the standard
// library debug/pe. The file is never mapped, executed, or loaded.
func inspectPE(rel, abs string) (imports []PEImport, symbolCount int, err error) {
	f, err := pe.Open(abs)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	syms, err := f.ImportedSymbols()
	if err != nil {
		return nil, 0, fmt.Errorf("import table: %w", err)
	}
	symbolCount = len(syms)
	seen := map[string]bool{}
	for _, raw := range syms {
		sym, lib := splitImported(raw)
		lower := strings.ToLower(sym)
		canon, severity := criticalImportSymbols[lower], "critical"
		if canon == "" {
			canon, severity = reviewImportSymbols[lower], "review"
		}
		if canon == "" {
			continue
		}
		key := strings.ToLower(lib + "!" + canon)
		if seen[key] {
			continue
		}
		seen[key] = true
		imports = append(imports, PEImport{
			Path:     filepath.ToSlash(rel),
			Library:  lib,
			Symbol:   canon,
			Severity: severity,
		})
	}
	return imports, symbolCount, nil
}

// splitImported accepts debug/pe's "CreateRemoteThread:KERNEL32.dll" format
// as well as a bare symbol used by some import layouts.
func splitImported(raw string) (symbol, library string) {
	raw = strings.TrimSpace(raw)
	if i := strings.LastIndex(raw, ":"); i >= 0 {
		return strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+1:])
	}
	return raw, ""
}

func isNativeArtifact(path string) bool {
	return NativeExtensions[strings.ToLower(filepath.Ext(path))]
}

func statSize(abs string) int64 {
	if st, err := os.Stat(abs); err == nil {
		return st.Size()
	}
	return 0
}
