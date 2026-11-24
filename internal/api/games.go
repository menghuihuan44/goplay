package api

import (
	"net/http"
	"runtime"
	"strings"

	"goplay/internal/games"
)

type windowsGamesBody struct {
	Platform string       `json:"platform"`
	Games    []games.Game `json:"games"`
}

// windowsGames lists launchable Start Menu and Steam entries for the desktop
// picker. Discovery is local and read-only.
func (s *Server) windowsGames(w http.ResponseWriter, r *http.Request) {
	list := games.ListWindowsGames()
	if list == nil {
		list = []games.Game{}
	}
	writeJSON(w, http.StatusOK, windowsGamesBody{Platform: runtime.GOOS, Games: list})
}

// launchWindowsGame accepts only an opaque ID produced by windowsGames. The
// server rescans installed entries before launching, so arbitrary commands and
// paths from the browser are rejected.
func (s *Server) launchWindowsGame(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "windows" {
		writeError(w, http.StatusNotImplemented, "仅 Windows 支持启动本机游戏")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	g, err := games.LaunchWindowsGame(req.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": g.ID, "name": g.Name, "kind": g.Kind})
}
