package audit

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func put16(b []byte, off int, v uint16) { binary.LittleEndian.PutUint16(b[off:], v) }
func put32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }

// buildPE creates a minimal valid PE32 image with one .text section and a
// single-symbol import table. It exists so the PE test never needs a compiler
// or a real (possibly malicious) sample.
func buildPE(t *testing.T, dll, symbol string) []byte {
	t.Helper()
	b := make([]byte, 0x400)
	copy(b[0:], "MZ")
	put32(b, 0x3C, 0x40)
	copy(b[0x40:], "PE\x00\x00")
	put16(b, 0x44, 0x14c) // IMAGE_FILE_MACHINE_I386
	put16(b, 0x46, 1)     // NumberOfSections
	put16(b, 0x54, 0xE0)  // SizeOfOptionalHeader
	put16(b, 0x56, 0x0102)
	opt := 0x58
	put16(b, opt+0, 0x10b) // PE32
	put32(b, opt+4, 0x200) // SizeOfCode
	put32(b, opt+8, 0x200)
	put32(b, opt+16, 0x1000) // AddressOfEntryPoint
	put32(b, opt+20, 0x1000) // BaseOfCode
	put32(b, opt+24, 0x2000) // BaseOfData
	put32(b, opt+28, 0x400000)
	put32(b, opt+32, 0x1000) // SectionAlignment
	put32(b, opt+36, 0x200)  // FileAlignment
	put16(b, opt+40, 4)
	put16(b, opt+48, 4)
	put32(b, opt+56, 0x2000) // SizeOfImage
	put32(b, opt+60, 0x200)  // SizeOfHeaders
	put16(b, opt+68, 3)      // Subsystem
	put32(b, opt+72, 0x100000)
	put32(b, opt+76, 0x1000)
	put32(b, opt+80, 0x100000)
	put32(b, opt+84, 0x1000)
	put32(b, opt+92, 16) // NumberOfRvaAndSizes
	// DataDirectory[1] = import, 20 bytes into optional header data dirs.
	put32(b, opt+96+8, 0x1100)
	put32(b, opt+96+12, 0x28)
	sec := opt + 0xE0
	copy(b[sec:], ".text")
	put32(b, sec+8, 0x200)
	put32(b, sec+12, 0x1000)
	put32(b, sec+16, 0x200)
	put32(b, sec+20, 0x200)
	put32(b, sec+36, 0x60000020)

	// Import descriptor at RVA 0x1100 -> raw offset 0x300.
	desc := 0x300
	put32(b, desc+0, 0x1140)  // OriginalFirstThunk (ILT)
	put32(b, desc+12, 0x1160) // Name
	put32(b, desc+16, 0x1150) // FirstThunk (IAT)
	// Second descriptor stays zero.
	ilt := 0x340
	put32(b, ilt, 0x1180)
	put32(b, ilt+4, 0)
	iat := 0x350
	put32(b, iat, 0x1180)
	put32(b, iat+4, 0)
	copy(b[0x360:], dll+"\x00")
	put16(b, 0x380, 0)
	copy(b[0x382:], symbol+"\x00")
	return b
}

func TestInspectPEClean(t *testing.T) {
	p := filepath.Join(t.TempDir(), "clean.exe")
	if err := os.WriteFile(p, buildPE(t, "KERNEL32.dll", "GetProcAddress"), 0o644); err != nil {
		t.Fatal(err)
	}
	imports, syms, err := inspectPE("clean.exe", p)
	if err != nil {
		t.Fatalf("inspectPE: %v", err)
	}
	if syms != 1 {
		t.Fatalf("imported symbols = %d, want 1", syms)
	}
	if len(imports) != 0 {
		t.Fatalf("unexpected findings: %+v", imports)
	}
}

func TestInspectPEForbiddenImport(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.exe")
	if err := os.WriteFile(p, buildPE(t, "KERNEL32.dll", "CreateRemoteThread"), 0o644); err != nil {
		t.Fatal(err)
	}
	imports, _, err := inspectPE("bad.exe", p)
	if err != nil {
		t.Fatalf("inspectPE: %v", err)
	}
	if len(imports) != 1 {
		t.Fatalf("findings = %d, want 1 (%+v)", len(imports), imports)
	}
	if imports[0].Symbol != "CreateRemoteThread" || imports[0].Library != "KERNEL32.dll" {
		t.Fatalf("unexpected import: %+v", imports[0])
	}
}

func TestRunFindsSourceViolationsAndPEImports(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("clean.go", "package demo\n\nfunc Play() {}\n")
	write("tools/bad.lua", "local h = OpenProcess(0x1F0FFF, false, pid)\n")
	write("native/inject.c", "void x(){ WriteProcessMemory(h, a, b, n, 0); }\n")
	write("client/Bad.kt", "val h = CreateRemoteThread(h, 0, 0, addr, 0, 0, 0)\n")
	write("support/fixture.lua", "OpenProcess(1, false, 2)\n")
	write("allowed/hook.go", "// SetWindowsHookEx is only mentioned in a comment\npackage allowed\n")
	write("notes.md", "OpenProcess is described here but markdown is not code\n")
	write("app.exe", string(buildPE(t, "ADVAPI32.dll", "WriteProcessMemory")))
	write("fine.exe", string(buildPE(t, "KERNEL32.dll", "GetProcAddress")))

	opts := DefaultOptions(root)
	opts.Allow = append(opts.Allow, "allowed/**")
	opts.SupportDirs = append(opts.SupportDirs, "support")
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Verdict != "FAIL" {
		t.Fatalf("verdict = %s, want FAIL", rep.Verdict)
	}
	if len(rep.SourceFindings) != 3 {
		t.Fatalf("source findings = %d, want 3: %+v", len(rep.SourceFindings), rep.SourceFindings)
	}
	got := map[string]bool{}
	for _, f := range rep.SourceFindings {
		got[f.Path] = true
		if f.Line != 1 || f.Column < 1 || f.Snippet == "" {
			t.Fatalf("bad finding location: %+v", f)
		}
	}
	for _, p := range []string{"tools/bad.lua", "native/inject.c", "client/Bad.kt"} {
		if !got[p] {
			t.Fatalf("missing finding for %s", p)
		}
	}
	if len(rep.PEImports) != 1 || rep.PEImports[0].Path != "app.exe" {
		t.Fatalf("PE imports = %+v, want app.exe WriteProcessMemory", rep.PEImports)
	}
	if rep.Stats.NativeFiles != 2 || rep.Stats.NativeInvalid != 0 {
		t.Fatalf("native stats = %+v", rep.Stats)
	}
	if rep.Stats.Allowlisted == 0 || rep.Stats.SupportSkipped == 0 {
		t.Fatalf("allow/support not counted: %+v", rep.Stats)
	}
}

func TestRunCleanTreePasses(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.exe"), buildPE(t, "KERNEL32.dll", "GetProcAddress"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), DefaultOptions(root))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Verdict != "PASS" {
		t.Fatalf("verdict = %s, findings=%+v imports=%+v", rep.Verdict, rep.SourceFindings, rep.PEImports)
	}
	if rep.Stats.SourceFiles != 1 || rep.Stats.NativeFiles != 1 {
		t.Fatalf("stats = %+v", rep.Stats)
	}
}

func TestMatchAllow(t *testing.T) {
	cases := []struct {
		rel  string
		pat  string
		want bool
	}{
		{"internal/audit/source.go", "internal/audit/**", true},
		{"internal/audit", "internal/audit/**", true},
		{"cmd/x/main.go", "internal/audit/**", false},
		{"internal/audit/testdata/a.lua", "**/testdata/**", true},
		{"a/b/c.go", "**/*.go", true},
		{"a/b/c.go", "a/*.go", false},
	}
	for _, c := range cases {
		if got := matchAllow(c.rel, []string{c.pat}); got != c.want {
			t.Fatalf("matchAllow(%q, %q) = %v, want %v", c.rel, c.pat, got, c.want)
		}
	}
}
func TestScanSourceFileIgnoresComments(t *testing.T) {
	cases := []struct {
		name string
		rel  string
		src  string
		want int
	}{
		{
			name: "go line comment",
			rel:  "a.go",
			src:  "// Never call OpenProcess here.\npackage demo\n",
			want: 0,
		},
		{
			name: "go block comment",
			rel:  "b.go",
			src:  "/*\nReadProcessMemory and WriteProcessMemory\nare forbidden.\n*/\npackage demo\n",
			want: 0,
		},
		{
			// String literals are not comments: keeping them flagged catches
			// dynamic resolution such as GetProcAddress("OpenProcess").
			name: "go raw string literal still flagged",
			rel:  "c.go",
			src:  "package demo\nvar s = `// OpenProcess`\n",
			want: 1,
		},
		{
			name: "lua line and long comment",
			rel:  "d.lua",
			src:  "-- OpenProcess\n--[[ CreateRemoteThread\nQueueUserAPC ]]\nprint('ok')\n",
			want: 0,
		},
		{
			name: "c block comment then real call",
			rel:  "e.c",
			src:  "/* OpenProcess is forbidden */\nvoid f(){ WriteProcessMemory(h,a,b,n,0); }\n",
			want: 1,
		},
		{
			name: "kotlin line comment",
			rel:  "f.kt",
			src:  "// OpenProcess(h)\nval x = 1\n",
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scanSourceFile(c.rel, c.src, DefaultRules())
			if len(got) != c.want {
				t.Fatalf("findings = %d, want %d: %+v", len(got), c.want, got)
			}
		})
	}
}

func TestRunTreatsOpenProcessImportAsReview(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.exe"), buildPE(t, "KERNEL32.dll", "OpenProcess"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), DefaultOptions(root))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Verdict != "PASS" {
		t.Fatalf("verdict = %s", rep.Verdict)
	}
	if rep.Stats.PEReview != 1 || rep.Stats.PECritical != 0 {
		t.Fatalf("stats = %+v", rep.Stats)
	}
	if len(rep.PEImports) != 1 || rep.PEImports[0].Severity != "review" {
		t.Fatalf("imports = %+v", rep.PEImports)
	}
}
