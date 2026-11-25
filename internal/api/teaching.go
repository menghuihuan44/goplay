package api

import (
	"net/http"
	"os"
)

// instanceTeaching returns learned demonstrations and the live recorder status.
func (s *Server) instanceTeaching(w http.ResponseWriter, r *http.Request) {
	info, err := s.mgr.TeachInfo(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// updateTeaching enables or disables learning from human takeover.
func (s *Server) updateTeaching(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	if err := s.mgr.SetTeaching(r.PathValue("id"), *req.Enabled); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := s.mgr.TeachInfo(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// clearTeaching forgets every learned demonstration for one instance.
func (s *Server) clearTeaching(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.ClearTeaching(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

// teachFrame serves one stored before/after thumbnail. The path must be a
// relative path returned by the teaching API; the manager validates it against
// the instance's own teaching directory.
func (s *Server) teachFrame(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := s.mgr.TeachImage(r.PathValue("id"), rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}
