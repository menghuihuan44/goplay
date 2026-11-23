package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Rule is one forbidden-symbol pattern. All default rules are critical: the
// product is an external game player and must never touch another process.
type Rule struct {
	ID          string
	Pattern     string
	Description string
	re          *regexp.Regexp
}

func (r Rule) regexp() *regexp.Regexp {
	if r.re != nil {
		return r.re
	}
	return regexp.MustCompile(r.Pattern)
}

// DefaultRules are the process-injection and memory-tampering primitives that
// are forbidden in every source language shipped or embedded by goplay.
func DefaultRules() []Rule {
	syms := []string{
		"OpenProcess",
		"ReadProcessMemory",
		"WriteProcessMemory",
		"CreateRemoteThread",
		"CreateRemoteThreadEx",
		"VirtualProtectEx",
		"VirtualAllocEx",
		"NtReadVirtualMemory",
		"NtWriteVirtualMemory",
		"NtCreateThreadEx",
		"NtMapViewOfSection",
		"QueueUserAPC",
		"SetThreadContext",
		"LdrLoadDll",
		"DLLInject",
		"InjectDLL",
	}
	rules := make([]Rule, 0, len(syms)+1)
	for _, sym := range syms {
		rules = append(rules, Rule{
			ID:          "forbidden-symbol/" + strings.ToLower(sym),
			Pattern:     `(?i)\b` + regexp.QuoteMeta(sym) + `\b`,
			Description: "process injection / memory tampering primitive " + sym,
		})
	}
	rules = append(rules, Rule{
		ID:          "forbidden-symbol/setwindowshookex",
		Pattern:     `(?i)\bSetWindowsHookEx[A-W]?\b`,
		Description: "global Windows hook installation",
	})
	for i := range rules {
		rules[i].re = regexp.MustCompile(rules[i].Pattern)
	}
	return rules
}

// SourceExtensions are the source languages the audit understands.
var SourceExtensions = map[string]bool{
	".go": true, ".c": true, ".h": true, ".cc": true, ".cpp": true,
	".cxx": true, ".hpp": true, ".hh": true, ".kt": true, ".kts": true,
	".lua": true,
}

// NativeExtensions are native artifacts whose PE import table is inspected.
var NativeExtensions = map[string]bool{
	".exe": true, ".dll": true, ".sys": true, ".ocx": true, ".efi": true, ".node": true,
}

var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
	".venv": true, "venv": true, "__pycache__": true, ".idea": true,
}

// Finding is one forbidden symbol occurrence in source.
type Finding struct {
	Rule     string `json:"rule"`
	Symbol   string `json:"symbol"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Snippet  string `json:"snippet"`
	Severity string `json:"severity"`
	Language string `json:"language"`
}

func languageOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".lua":
		return "lua"
	case ".kt", ".kts":
		return "kotlin"
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh":
		return "c/c++"
	}
	return "unknown"
}

func scanSourceFile(rel, content string, rules []Rule) []Finding {
	var out []Finding
	rawLines := strings.Split(content, "\n")
	// Strip comments before matching so a comment that merely names a forbidden
	// API (e.g. "do not call OpenProcess") is not reported as a violation.
	masked := strings.Split(maskComments(content, languageOf(rel)), "\n")
	for li, line := range masked {
		raw := ""
		if li < len(rawLines) {
			raw = rawLines[li]
		}
		for _, rule := range rules {
			locs := rule.regexp().FindAllStringIndex(line, -1)
			for _, loc := range locs {
				out = append(out, Finding{
					Rule:     rule.ID,
					Symbol:   line[loc[0]:loc[1]],
					Path:     filepath.ToSlash(rel),
					Line:     li + 1,
					Column:   loc[0] + 1,
					Snippet:  snippet(raw),
					Severity: "critical",
					Language: languageOf(rel),
				})
			}
		}
	}
	return out
}

// maskComments blanks out comments while preserving byte offsets and newlines,
// so regex matches and reported columns still line up with the original file.
// String and character literals are kept intact: a literal such as "OpenProcess"
// is masked only when it is inside a comment, not when it is source code.
func maskComments(src, lang string) string {
	out := []byte(src)
	n := len(out)
	block := false
	line := false
	var quote byte
	escaped := false
	mask := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	for i := 0; i < n; {
		c := src[i]
		if line {
			if c == '\n' {
				line = false
			} else {
				mask(i)
			}
			i++
			continue
		}
		if block {
			if c == '*' && i+1 < n && src[i+1] == '/' {
				mask(i)
				mask(i + 1)
				block = false
				i += 2
				continue
			}
			mask(i)
			i++
			continue
		}
		if quote != 0 {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			i++
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			i++
			continue
		}
		if lang == "go" && c == '`' {
			// Raw string: no escapes or comments inside; keep it as-is.
			i++
			for i < n && src[i] != '`' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if lang == "lua" {
			if c == '-' && i+1 < n && src[i+1] == '-' {
				// Long comment --[[ ... ]] or --[=[ ... ]=].
				if i+2 < n && src[i+2] == '[' {
					eq := 0
					j := i + 3
					for j < n && src[j] == '=' {
						eq++
						j++
					}
					if j < n && src[j] == '[' {
						closeSeq := "]" + strings.Repeat("=", eq) + "]"
						end := n
						if k := strings.Index(src[j+1:], closeSeq); k >= 0 {
							end = j + 1 + k + len(closeSeq)
						}
						for ; i < end; i++ {
							mask(i)
						}
						continue
					}
				}
				line = true
				mask(i)
				mask(i + 1)
				i += 2
				continue
			}
		} else {
			if c == '/' && i+1 < n && src[i+1] == '/' {
				line = true
				mask(i)
				mask(i + 1)
				i += 2
				continue
			}
			if c == '/' && i+1 < n && src[i+1] == '*' {
				block = true
				mask(i)
				mask(i + 1)
				i += 2
				continue
			}
		}
		i++
	}
	return string(out)
}

func snippet(line string) string {
	line = strings.TrimSpace(strings.ReplaceAll(line, "\t", " "))
	line = strings.Join(strings.Fields(line), " ")
	const max = 200
	if len(line) > max {
		line = line[:max] + "..."
	}
	return line
}

// matchAllow reports whether rel is covered by an allowlist pattern.
// Patterns use slash separators; "**" spans directories, "*" and "?" do not.
// A pattern that ends in "/" or "/**" is a directory prefix.
func matchAllow(rel string, patterns []string) bool {
	rel = filepath.ToSlash(rel)
	for _, raw := range patterns {
		pat := filepath.ToSlash(strings.TrimSpace(raw))
		if pat == "" {
			continue
		}
		if strings.HasSuffix(pat, "/**") {
			prefix := strings.TrimSuffix(pat, "/**")
			if !strings.ContainsAny(prefix, "*?") {
				if prefix == "" || rel == prefix || strings.HasPrefix(rel, prefix+"/") {
					return true
				}
				continue
			}
		}
		if strings.HasSuffix(pat, "/") {
			if strings.HasPrefix(rel, pat) {
				return true
			}
			continue
		}
		if globMatch(pat, rel) {
			return true
		}
	}
	return false
}

func globMatch(pattern, name string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

func underAny(rel string, prefixes []string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range prefixes {
		p = strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(p)), "/")
		if p == "" {
			continue
		}
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Path != f[j].Path {
			return f[i].Path < f[j].Path
		}
		if f[i].Line != f[j].Line {
			return f[i].Line < f[j].Line
		}
		return f[i].Column < f[j].Column
	})
}

func readSource(abs string, maxBytes int64) ([]byte, bool, error) {
	st, err := os.Stat(abs)
	if err != nil {
		return nil, false, err
	}
	if maxBytes > 0 && st.Size() > maxBytes {
		return nil, true, fmt.Errorf("file is %d bytes, over the %d byte scan limit", st.Size(), maxBytes)
	}
	b, err := os.ReadFile(abs)
	return b, false, err
}
