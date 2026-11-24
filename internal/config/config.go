package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// RouteConfig mirrors llm.RouteConfig without importing the llm package.
// app.RoutesFromConfig converts between the two representations.
type RouteConfig struct {
	Mode  string `json:"mode"`
	Model string `json:"model,omitempty"`
}

type LLMConfig struct {
	Mode        string                 `json:"mode"` // auto, api, local, offline
	BaseURL     string                 `json:"base_url"`
	APIKey      string                 `json:"api_key"`
	Model       string                 `json:"model"`
	Temperature float64                `json:"temperature"`
	MaxTokens   int                    `json:"max_tokens"`
	TimeoutMS   int                    `json:"timeout_ms"`
	Vision      bool                   `json:"vision"`
	Routes      map[string]RouteConfig `json:"routes,omitempty"`
}

type LocalConfig struct {
	ModelsDir     string  `json:"models_dir"`
	RuntimePath   string  `json:"runtime_path"`
	ServerPort    int     `json:"server_port"`
	AutoDownload  bool    `json:"auto_download"`
	PreferredTier string  `json:"preferred_tier"`
	ContextTokens int     `json:"context_tokens"`
	Threads       int     `json:"threads"`
	TTFTBudgetMS  int     `json:"ttft_budget_ms"`
	TPSFloor      float64 `json:"tps_floor"`
}

type Config struct {
	Addr           string      `json:"addr"`
	DataDir        string      `json:"data_dir"`
	TickMS         int         `json:"tick_ms"`
	HumanResumeMS  int         `json:"human_resume_ms"`
	MaxInstances   int         `json:"max_instances"`
	MaxActionRate  int         `json:"max_actions_per_sec"`
	MemoryBudgetKB int         `json:"memory_budget_kb"`
	AutoStart      bool        `json:"auto_start"`
	OverlayURL     string      `json:"overlay_url"`
	LLM            LLMConfig   `json:"llm"`
	Local          LocalConfig `json:"local"`
	Version        string      `json:"version,omitempty"`
}

func DefaultDataDir() string {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "GoPlay")
		}
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "goplay")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".goplay")
}

func Default() Config {
	data := DefaultDataDir()
	return Config{
		Addr:           "127.0.0.1:8420",
		DataDir:        data,
		TickMS:         250,
		HumanResumeMS:  2500,
		MaxInstances:   8,
		MaxActionRate:  8,
		MemoryBudgetKB: 256,
		AutoStart:      true,
		OverlayURL:     "http://127.0.0.1:8420/overlay",
		LLM: LLMConfig{
			Mode:        "auto",
			BaseURL:     "https://api.openai.com/v1",
			Model:       "gpt-4o-mini",
			Temperature: 0.2,
			MaxTokens:   512,
			TimeoutMS:   45000,
			Vision:      false,
		},
		Local: LocalConfig{
			ModelsDir:     filepath.Join(data, "models"),
			RuntimePath:   "",
			ServerPort:    8421,
			AutoDownload:  false,
			PreferredTier: "auto",
			ContextTokens: 4096,
			Threads:       4,
			TTFTBudgetMS:  1500,
			TPSFloor:      8,
		},
		Version: "0.1.0",
	}
}

// Load reads config from path (if it exists) on top of defaults, then applies
// environment overrides. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = filepath.Join(cfg.DataDir, "config.json")
	}
	b, err := os.ReadFile(path)
	if err == nil {
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return cfg, err
	}
	applyEnv(&cfg)
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	if cfg.Local.ModelsDir == "" {
		cfg.Local.ModelsDir = filepath.Join(cfg.DataDir, "models")
	}
	if cfg.OverlayURL == "" {
		cfg.OverlayURL = "http://" + cfg.Addr + "/overlay"
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.TickMS < 20 {
		c.TickMS = 20
	}
	if c.TickMS > 60000 {
		c.TickMS = 60000
	}
	if c.MaxInstances <= 0 {
		c.MaxInstances = 1
	}
	if c.MaxActionRate <= 0 {
		c.MaxActionRate = 1
	}
	if c.MemoryBudgetKB < 16 {
		c.MemoryBudgetKB = 16
	}
	switch c.LLM.Mode {
	case "auto", "api", "local", "offline":
	default:
		return fmt.Errorf("invalid llm.mode %q (want auto|api|local|offline)", c.LLM.Mode)
	}
	for role, route := range c.LLM.Routes {
		role = strings.ToLower(strings.TrimSpace(role))
		mode := strings.ToLower(strings.TrimSpace(route.Mode))
		switch mode {
		case "", "auto", "api", "local", "offline":
		default:
			return fmt.Errorf("invalid llm.routes[%s].mode %q (want auto|api|local|offline)", role, route.Mode)
		}
		route.Mode = mode
		route.Model = strings.TrimSpace(route.Model)
		c.LLM.Routes[role] = route
	}
	return nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("GOPLAY_ADDR"); v != "" {
		c.Addr = v
	}
	if v := os.Getenv("GOPLAY_DATA"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("GOPLAY_LLM_MODE"); v != "" {
		c.LLM.Mode = v
	}
	if v := os.Getenv("GOPLAY_API_BASE"); v != "" {
		c.LLM.BaseURL = v
	}
	if v := os.Getenv("GOPLAY_API_KEY"); v != "" {
		c.LLM.APIKey = v
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" && c.LLM.APIKey == "" {
		c.LLM.APIKey = v
	}
	if v := os.Getenv("GOPLAY_MODEL"); v != "" {
		c.LLM.Model = v
	}
	if v := os.Getenv("GOPLAY_THREADS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Local.Threads = n
		}
	}
}

func (c Config) Save(path string) error {
	if path == "" {
		path = filepath.Join(c.DataDir, "config.json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Redacted returns a copy safe to send to a browser overlay.
func (c Config) Redacted() Config {
	c.LLM.APIKey = ""
	return c
}
