package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) voiceView() map[string]any {
	v := s.cfg.Voice
	base := strings.TrimSpace(v.BaseURL)
	if base == "" {
		base = strings.TrimSpace(s.cfg.LLM.BaseURL)
	}
	note := "系统语音由手机/浏览器朗读；API 模式走 OpenAI 兼容的 /audio/speech 接口。"
	if v.TTSMode == "local" {
		note = "手机本机音色使用设备已安装的系统 TTS 引擎；GoPlay 不把神经语音模型打进安装包，可在系统语音设置里安装中文离线音色。"
	}
	return map[string]any{
		"enabled": v.Enabled, "tts_mode": v.TTSMode, "asr_mode": v.ASRMode,
		"base_url": v.BaseURL, "effective_base": base,
		"has_api_key": strings.TrimSpace(v.APIKey) != "",
		"model":       v.Model, "voice": v.Voice, "language": v.Language, "speed": v.Speed,
		"note": note,
	}
}

func (s *Server) getVoice(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.voiceView())
}

type voiceUpdateRequest struct {
	Enabled   *bool    `json:"enabled"`
	TTSMode   string   `json:"tts_mode"`
	ASRMode   string   `json:"asr_mode"`
	BaseURL   string   `json:"base_url"`
	ClearBase bool     `json:"clear_base"`
	APIKey    string   `json:"api_key"`
	ClearKey  bool     `json:"clear_key"`
	Model     string   `json:"model"`
	Voice     string   `json:"voice"`
	Language  string   `json:"language"`
	Speed     *float64 `json:"speed"`
}

func (s *Server) updateVoice(w http.ResponseWriter, r *http.Request) {
	var req voiceUpdateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.cfg
	if req.Enabled != nil {
		cfg.Voice.Enabled = *req.Enabled
	}
	if v := strings.ToLower(strings.TrimSpace(req.TTSMode)); v != "" {
		switch v {
		case "system", "api", "local":
			cfg.Voice.TTSMode = v
		default:
			writeError(w, http.StatusBadRequest, "tts_mode must be system, api or local")
			return
		}
	}
	if v := strings.ToLower(strings.TrimSpace(req.ASRMode)); v != "" {
		switch v {
		case "system", "off":
			cfg.Voice.ASRMode = v
		default:
			writeError(w, http.StatusBadRequest, "asr_mode must be system or off")
			return
		}
	}
	if req.ClearBase {
		cfg.Voice.BaseURL = ""
	} else if v := strings.TrimSpace(req.BaseURL); v != "" {
		cfg.Voice.BaseURL = strings.TrimRight(v, "/")
	}
	if req.ClearKey {
		cfg.Voice.APIKey = ""
	} else if v := strings.TrimSpace(req.APIKey); v != "" {
		cfg.Voice.APIKey = v
	}
	if v := strings.TrimSpace(req.Model); v != "" {
		cfg.Voice.Model = v
	}
	if v := strings.TrimSpace(req.Voice); v != "" {
		cfg.Voice.Voice = v
	}
	if v := strings.TrimSpace(req.Language); v != "" {
		cfg.Voice.Language = v
	}
	if req.Speed != nil {
		cfg.Voice.Speed = *req.Speed
	}
	if err := cfg.Save(filepath.Join(cfg.DataDir, "config.json")); err != nil {
		writeError(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	s.cfg = cfg
	writeJSON(w, http.StatusOK, s.voiceView())
}

// speakVoice proxies one TTS request so the API key never reaches the browser.
// System mode is a client-side operation and returns a clear 409 instead of
// pretending the backend spoke.
func (s *Server) speakVoice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if len([]rune(text)) > 2000 {
		text = string([]rune(text)[:2000])
	}
	v := s.cfg.Voice
	if !v.Enabled {
		writeError(w, http.StatusConflict, "voice is disabled")
		return
	}
	if v.TTSMode == "" || v.TTSMode == "system" || v.TTSMode == "local" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "system/local TTS is played by the device, not the core", "code": "system_tts",
		})
		return
	}
	base := strings.TrimSpace(v.BaseURL)
	key := strings.TrimSpace(v.APIKey)
	if base == "" {
		base, key = strings.TrimSpace(s.cfg.LLM.BaseURL), firstNonEmpty(key, s.cfg.LLM.APIKey)
	}
	if base == "" {
		writeError(w, http.StatusBadRequest, "voice base_url is not configured")
		return
	}
	model := firstNonEmpty(strings.TrimSpace(v.Model), "tts-1")
	voice := firstNonEmpty(strings.TrimSpace(v.Voice), "alloy")
	payload := map[string]any{
		"model": model, "input": text, "voice": voice, "response_format": "mp3",
	}
	if v.Speed > 0 {
		payload["speed"] = v.Speed
	}
	body, _ := json.Marshal(payload)
	endpoint := strings.TrimRight(base, "/")
	if !strings.HasSuffix(endpoint, "/audio/speech") {
		endpoint += "/audio/speech"
	}
	hreq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hreq.Header.Set("Content-Type", "application/json")
	if key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(hreq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tts request: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		writeError(w, http.StatusBadGateway, fmt.Sprintf("tts upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(b))))
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-GoPlay-TTS", model)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 16<<20))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
