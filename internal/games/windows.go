//go:build windows

// Package games discovers games that are already installed on this machine so
// the desktop console can offer a real picker instead of a free-text field.
// It never touches a running game's memory or process: discovery reads Start
// Menu shortcuts and Steam manifests, and launching delegates to the shell.
package games

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Game is one launchable entry shown in the picker. Target is deliberately not
// serialized: the browser only needs the opaque ID and sends it back to launch.
type Game struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Target string `json:"-"`
}

var (
	nameRE      = regexp.MustCompile(`(?m)^\s*"name"\s+"([^"]*)"`)
	appRE       = regexp.MustCompile(`(?m)^\s*"appid"\s+"([0-9]+)"`)
	flagRE      = regexp.MustCompile(`(?m)^\s*"stateflags"\s+"([0-9]+)"`)
	pathRE      = regexp.MustCompile(`(?m)^\s*"path"\s+"([^"]*)"`)
	skipRE      = regexp.MustCompile(`(?i)(uninstall|卸载|remove |readme|help|manual|website|官网|support|documentation|license|更新|updater|repair|修复|settings|设置|配置|command prompt|powershell|terminal|system tools|系统工具|accessories|附件)`)
	shortcutExt = map[string]bool{".lnk": true, ".url": true, ".appref-ms": true}
	skipDir     = map[string]bool{
		"startup": true, "accessories": true, "system tools": true,
		"windows system": true, "windows administrative tools": true,
		"windows powershell": true, "administrative tools": true,
	}
)

func gameID(kind, target string) string {
	sum := sha1.Sum([]byte(kind + "|" + strings.ToLower(target)))
	return "win-" + hex.EncodeToString(sum[:])[:12]
}

// ListWindowsGames returns Start Menu shortcuts plus installed Steam apps. It
// is best-effort: unreadable directories are skipped rather than failing the
// whole picker.
func ListWindowsGames() []Game {
	seen := map[string]bool{} // lower name + kind, to avoid Start Menu duplicates
	byTarget := map[string]bool{}
	out := make([]Game, 0, 64)
	add := func(g Game) {
		key := strings.ToLower(strings.TrimSpace(g.Name)) + "|" + g.Kind
		if g.Name == "" || seen[key] || byTarget[strings.ToLower(g.Target)] {
			return
		}
		seen[key] = true
		byTarget[strings.ToLower(g.Target)] = true
		out = append(out, g)
	}

	for _, g := range steamGames() {
		add(g)
	}
	for _, g := range startMenuGames() {
		add(g)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a == b {
			return out[i].ID < out[j].ID
		}
		return a < b
	})
	if len(out) > 300 {
		out = out[:300]
	}
	return out
}

func launchDirs() []string {
	var dirs []string
	if v := os.Getenv("ProgramData"); v != "" {
		dirs = append(dirs, filepath.Join(v, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	if v := os.Getenv("APPDATA"); v != "" {
		dirs = append(dirs, filepath.Join(v, "Microsoft", "Windows", "Start Menu", "Programs"))
	}
	return dirs
}

func startMenuGames() []Game {
	var out []Game
	for _, root := range launchDirs() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if !shortcutExt[ext] {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			for _, part := range strings.Split(rel, string(os.PathSeparator)) {
				if skipDir[strings.ToLower(part)] {
					return nil
				}
			}
			name := strings.TrimSpace(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())))
			if name == "" || skipRE.MatchString(name) {
				return nil
			}
			out = append(out, Game{
				ID: gameID("shortcut", path), Name: name, Kind: "shortcut",
				Source: "开始菜单", Target: path,
			})
			return nil
		})
	}
	return out
}

func steamRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		p = strings.TrimSpace(strings.Trim(p, "\""))
		if p == "" || seen[strings.ToLower(p)] {
			return
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			seen[strings.ToLower(p)] = true
			roots = append(roots, p)
		}
	}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "ProgramW6432"} {
		if base := os.Getenv(env); base != "" {
			add(filepath.Join(base, "Steam"))
		}
	}
	// libraryfolders.vdf lists every additional Steam library.
	for _, root := range append([]string{}, roots...) {
		data, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, m := range pathRE.FindAllStringSubmatch(string(data), -1) {
			add(strings.ReplaceAll(m[1], "\\\\", "\\"))
		}
	}
	return roots
}

func steamGames() []Game {
	seen := map[string]bool{}
	var out []Game
	for _, root := range steamRoots() {
		manifestDir := filepath.Join(root, "steamapps")
		entries, err := os.ReadDir(manifestDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if e.IsDir() || !strings.HasPrefix(n, "appmanifest_") || !strings.HasSuffix(n, ".acf") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(manifestDir, e.Name()))
			if err != nil {
				continue
			}
			name := firstMatch(nameRE, string(data))
			appid := firstMatch(appRE, string(data))
			if name == "" || appid == "" || seen[appid] {
				continue
			}
			// stateflags 4 means fully installed; older manifests omit it.
			if flags := firstMatch(flagRE, string(data)); flags != "" {
				if v, err := strconv.Atoi(flags); err == nil && v&4 == 0 {
					continue
				}
			}
			seen[appid] = true
			out = append(out, Game{
				ID: gameID("steam", appid), Name: name, Kind: "steam",
				Source: "Steam", Target: "steam://rungameid/" + appid,
			})
		}
	}
	return out
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// LaunchWindowsGame validates an opaque picker ID against a fresh scan, then
// hands the exact discovered target to the Windows shell. The browser can never
// pass a raw path or command.
func LaunchWindowsGame(id string) (Game, error) {
	for _, g := range ListWindowsGames() {
		if g.ID != id {
			continue
		}
		if g.Kind == "steam" {
			if !strings.HasPrefix(g.Target, "steam://rungameid/") {
				return Game{}, fmt.Errorf("invalid steam target")
			}
		} else {
			ext := strings.ToLower(filepath.Ext(g.Target))
			if !shortcutExt[ext] {
				return Game{}, fmt.Errorf("invalid shortcut target")
			}
			if st, err := os.Stat(g.Target); err != nil || st.IsDir() {
				return Game{}, fmt.Errorf("shortcut no longer exists")
			}
		}
		cmd := exec.Command("cmd", "/c", "start", "", g.Target)
		if err := cmd.Start(); err != nil {
			return Game{}, err
		}
		go func() { _ = cmd.Wait() }()
		return g, nil
	}
	return Game{}, fmt.Errorf("game not found; refresh the list")
}
