package llm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	System      string
	Prompt      string
	Images      []string // base64 PNG/JPEG, no data: prefix
	Temperature float64
	MaxTokens   int
	JSON        bool
}

type Response struct {
	Text             string
	PromptTokens     int
	CompletionTokens int
	Model            string
	Provider         string
	Latency          time.Duration
	TTFT             time.Duration
}

type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
	Available(ctx context.Context) bool
}

type StreamProvider interface {
	Provider
	// CompleteStream calls onToken for each delta; ttft is measured from the
	// request start to the first non-empty delta.
	CompleteStream(ctx context.Context, req Request, onToken func(delta string, ttft time.Duration)) (Response, error)
}

var ErrNoProvider = errors.New("no LLM provider available")

// Role identifies one stage of the game-playing pipeline. Every role can be
// routed to the API or to the local model independently.
const (
	RolePerception = "perception"
	RoleAnalysis   = "analysis"
	RoleStrategy   = "strategy"
	RoleLanguage   = "language"
	RoleMemory     = "memory"
	RoleValidation = "validation"
)

// Roles returns every routable role in pipeline order.
func Roles() []string {
	return []string{RolePerception, RoleAnalysis, RoleStrategy, RoleLanguage, RoleMemory, RoleValidation}
}

// ValidRole reports whether role (case-insensitive) is routable.
func ValidRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	for _, known := range Roles() {
		if role == known {
			return true
		}
	}
	return false
}

// RouteConfig overrides the global mode for one role. An empty Mode inherits
// the router's global mode. Model only affects API routes, except for local
// routes where it names the model the loopback server must be running.
type RouteConfig struct {
	Mode  string `json:"mode"`
	Model string `json:"model,omitempty"`
}

// ModelProvider is implemented by providers that can report the model they
// serve; the router refuses a local route whose model is not the running one.
type ModelProvider interface {
	Provider
	Model() string
}

// ModelCloner is implemented by providers that can derive a copy pinned to a
// different model name, so API routes can honor RouteConfig.Model.
type ModelCloner interface {
	Provider
	WithModel(model string) Provider
}

// Router is the single decision point for API-vs-local providers. It is
// mutable at runtime so the model manager can attach a llama.cpp provider after
// a download finishes; every field is guarded by mu.
//
// local is the default local provider. locals holds one provider per running
// local model, keyed by model id, so several llama-server processes can serve
// different roles at the same time; RouteConfig.Model picks the one a role
// must use.
type Router struct {
	mu     sync.RWMutex
	mode   string
	api    Provider
	local  Provider
	locals map[string]Provider
	routes map[string]RouteConfig

	statMu sync.Mutex
	stats  map[string]*roleStat
}

// roleStat is the runtime evidence that a role actually executed. It is
// deliberately in-memory: routing health is a live signal, not game memory.
type roleStat struct {
	Calls       int64
	Errors      int64
	LastAt      time.Time
	LastModel   string
	LastProv    string
	LastLatency time.Duration
	LastError   string
}

func NewRouter(mode string, api, local Provider) *Router {
	if mode == "" {
		mode = "auto"
	}
	return &Router{
		mode:   mode,
		api:    api,
		local:  local,
		locals: make(map[string]Provider),
		routes: make(map[string]RouteConfig),
		stats:  make(map[string]*roleStat),
	}
}

// Set replaces the mode and both providers atomically.
func (r *Router) Set(mode string, api, local Provider) {
	if mode == "" {
		mode = "auto"
	}
	r.mu.Lock()
	r.mode, r.api, r.local = mode, api, local
	r.mu.Unlock()
}

// SetMode switches api/local/auto/offline without touching providers.
func (r *Router) SetMode(mode string) {
	if mode == "" {
		mode = "auto"
	}
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
}

// SetAPI swaps the remote provider.
func (r *Router) SetAPI(p Provider) {
	r.mu.Lock()
	r.api = p
	r.mu.Unlock()
}

// SetLocal swaps the default on-device provider. It stays the provider used by
// roles that do not name a model, and the fallback when a named model is not
// running.
func (r *Router) SetLocal(p Provider) {
	r.mu.Lock()
	r.local = p
	r.mu.Unlock()
}

// SetLocalModels installs one provider per running local model, keyed by model
// id. A role whose RouteConfig.Model names a registered model is routed to that
// provider, case-insensitively. The map is copied; a nil entry is ignored.
func (r *Router) SetLocalModels(models map[string]Provider) {
	clean := make(map[string]Provider, len(models))
	for id, p := range models {
		id = strings.TrimSpace(id)
		if id == "" || p == nil {
			continue
		}
		clean[id] = p
	}
	r.mu.Lock()
	r.locals = clean
	r.mu.Unlock()
}

// LocalModels returns a copy of the running local model providers keyed by
// model id.
func (r *Router) LocalModels() map[string]Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Provider, len(r.locals))
	for id, p := range r.locals {
		out[id] = p
	}
	return out
}

// SetRoutes replaces the per-role overrides. Empty modes are inherited from
// the global mode; empty role entries (both fields empty) are dropped.
func (r *Router) SetRoutes(routes map[string]RouteConfig) {
	clean := make(map[string]RouteConfig, len(routes))
	for role, rc := range routes {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "" {
			continue
		}
		rc.Mode = strings.ToLower(strings.TrimSpace(rc.Mode))
		rc.Model = strings.TrimSpace(rc.Model)
		if rc.Mode == "" && rc.Model == "" {
			continue
		}
		clean[role] = rc
	}
	r.mu.Lock()
	r.routes = clean
	r.mu.Unlock()
}

// Routes returns a copy of the current per-role overrides.
func (r *Router) Routes() map[string]RouteConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]RouteConfig, len(r.routes))
	for role, rc := range r.routes {
		out[role] = rc
	}
	return out
}

func (r *Router) routeFor(role string) RouteConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.routes[strings.ToLower(strings.TrimSpace(role))]
}

// routeResolution is a fully resolved role: which provider candidates are
// usable and why when none is.
type routeResolution struct {
	mode  string
	model string
	api   Provider
	local Provider
	// fallback is the default local provider used by auto mode when the
	// explicitly requested local model is not usable.
	fallback Provider
	note     string
}

func (rr routeResolution) addNote(note string) routeResolution {
	if note == "" {
		return rr
	}
	if rr.note == "" {
		rr.note = note
	} else {
		rr.note += "; " + note
	}
	return rr
}

// resolveRoute reads the global mode and providers once and applies the role
// override. It never calls Available, so it is safe under concurrent updates.
func (r *Router) resolveRoute(role string) routeResolution {
	r.mu.RLock()
	global := r.mode
	api := r.api
	local := r.local
	locals := r.locals
	rc := r.routes[strings.ToLower(strings.TrimSpace(role))]
	r.mu.RUnlock()

	res := routeResolution{model: rc.Model}
	mode := global
	if rc.Mode != "" {
		mode = rc.Mode
	}
	switch mode {
	case "", "auto":
		res.mode = "auto"
	case "api", "local", "offline":
		res.mode = mode
	default:
		res.mode = global
		res = res.addNote(fmt.Sprintf("unknown route mode %q; inheriting global %q", mode, global))
	}
	switch res.mode {
	case "offline":
		return res
	case "api":
		res.api = apiCandidate(api, rc.Model, &res)
	case "local":
		res.local = localCandidateFor(locals, local, rc.Model, &res)
	case "auto":
		res.local = autoLocalCandidate(locals, local, rc.Model, &res)
		if rc.Model != "" {
			// A role that names a model still degrades through the default
			// local model before giving up on the API.
			if named, ok := lookupLocalModel(locals, rc.Model); ok && local != nil && !providersEqual(named, local) {
				res.fallback = local
			}
		}
		res.api = apiCandidate(api, rc.Model, &res)
	}
	return res
}

func (rr *routeResolution) notef(format string, args ...any) {
	note := fmt.Sprintf(format, args...)
	if rr.note == "" {
		rr.note = note
	} else {
		rr.note += "; " + note
	}
}

func apiCandidate(api Provider, model string, res *routeResolution) Provider {
	if api == nil {
		return nil
	}
	if model == "" {
		return api
	}
	cloner, ok := api.(ModelCloner)
	if !ok {
		res.notef("API provider %s cannot override model %q", api.Name(), model)
		return nil
	}
	return cloner.WithModel(model)
}

// lookupLocalModel returns the provider registered for a running local model.
// Model ids are matched case-insensitively.
func lookupLocalModel(locals map[string]Provider, model string) (Provider, bool) {
	model = strings.TrimSpace(model)
	if model == "" || len(locals) == 0 {
		return nil, false
	}
	if p, ok := locals[model]; ok && p != nil {
		return p, true
	}
	for id, p := range locals {
		if p != nil && strings.EqualFold(id, model) {
			return p, true
		}
	}
	return nil, false
}

// localCandidateFor resolves a strict local route: the named model first (from
// the multi-model map), then the legacy single-provider model check.
func localCandidateFor(locals map[string]Provider, local Provider, model string, res *routeResolution) Provider {
	model = strings.TrimSpace(model)
	if model == "" {
		return local
	}
	if p, ok := lookupLocalModel(locals, model); ok {
		return p
	}
	return localCandidate(local, model, res)
}

// autoLocalCandidate resolves the local side of an auto route. A registered
// model wins; otherwise the default local provider is used even when it serves
// another model, because auto mode degrades instead of failing.
func autoLocalCandidate(locals map[string]Provider, local Provider, model string, res *routeResolution) Provider {
	model = strings.TrimSpace(model)
	if model == "" {
		return local
	}
	if p, ok := lookupLocalModel(locals, model); ok {
		return p
	}
	if local == nil {
		res.notef("local model %q is not running (no local server)", model)
		return nil
	}
	if mp, ok := local.(ModelProvider); ok {
		running := strings.TrimSpace(mp.Model())
		if strings.EqualFold(running, model) {
			return local
		}
		res.notef("local model %q is not running (running %q); auto is using the default local model", model, running)
		return local
	}
	res.notef("default local provider cannot report its model; using it for %q", model)
	return local
}

func localCandidate(local Provider, model string, res *routeResolution) Provider {
	if local == nil {
		if model != "" {
			res.notef("local model %q is not running (no local server)", model)
		}
		return nil
	}
	if model == "" {
		return local
	}
	mp, ok := local.(ModelProvider)
	if !ok {
		res.notef("local provider cannot report its model; refusing model %q", model)
		return nil
	}
	running := strings.TrimSpace(mp.Model())
	if !strings.EqualFold(running, model) {
		res.notef("local model %q is not running (running %q)", model, running)
		return nil
	}
	return local
}

// providersEqual compares two providers without panicking on non-comparable
// dynamic types.
func providersEqual(a, b Provider) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb || !ta.Comparable() {
		return false
	}
	return a == b
}

// localChain lists the local providers an auto route may use, best first: the
// requested model, then the default local model.
func (rr routeResolution) localChain() []Provider {
	chain := make([]Provider, 0, 2)
	if rr.local != nil {
		chain = append(chain, rr.local)
	}
	if rr.fallback != nil && !providersEqual(rr.fallback, rr.local) {
		chain = append(chain, rr.fallback)
	}
	return chain
}

// availableLocal returns the first available local provider in chain order.
func (rr routeResolution) availableLocal(ctx context.Context) Provider {
	for _, p := range rr.localChain() {
		if p.Available(ctx) {
			return p
		}
	}
	return nil
}

func (rr routeResolution) reason() string {
	switch rr.mode {
	case "offline":
		return "role is offline"
	case "api":
		if rr.api == nil {
			if rr.note != "" {
				return rr.note
			}
			return "no API provider configured"
		}
	case "local":
		if rr.local == nil {
			if rr.note != "" {
				return rr.note
			}
			if rr.model != "" {
				return fmt.Sprintf("local model %q is not running", rr.model)
			}
			return "no local model server is running"
		}
	default:
		if rr.api == nil && rr.local == nil {
			if rr.note != "" {
				return rr.note
			}
			return "no API or local provider available"
		}
	}
	return rr.note
}

// For returns a Provider pinned to one role. It is never nil; when the route
// has no provider, Available reports false and Complete returns ErrNoProvider.
func (r *Router) For(role string) Provider {
	return &routeProvider{router: r, role: strings.ToLower(strings.TrimSpace(role))}
}

type routeProvider struct {
	router *Router
	role   string
}

func (p *routeProvider) Name() string {
	rr := p.router.resolveRoute(p.role)
	switch rr.mode {
	case "api":
		if rr.api != nil {
			return "route:" + p.role + "=" + rr.api.Name()
		}
	case "local":
		if rr.local != nil {
			return "route:" + p.role + "=" + rr.local.Name()
		}
	default:
		if local := rr.availableLocal(context.Background()); local != nil {
			return "route:" + p.role + "=" + local.Name()
		}
		if rr.api != nil {
			return "route:" + p.role + "=" + rr.api.Name()
		}
	}
	return "route:" + p.role + "=unavailable"
}

func (p *routeProvider) Available(ctx context.Context) bool {
	rr := p.router.resolveRoute(p.role)
	switch rr.mode {
	case "offline":
		return false
	case "api":
		return rr.api != nil && rr.api.Available(ctx)
	case "local":
		return rr.local != nil && rr.local.Available(ctx)
	default:
		if rr.availableLocal(ctx) != nil {
			return true
		}
		return rr.api != nil && rr.api.Available(ctx)
	}
}

func (p *routeProvider) Complete(ctx context.Context, req Request) (Response, error) {
	rr := p.router.resolveRoute(p.role)
	start := time.Now()
	finish := func(resp Response, err error) (Response, error) {
		model, provider := resp.Model, resp.Provider
		if model == "" {
			model = rr.model
		}
		if provider == "" && err == nil {
			provider = resp.Provider
		}
		p.router.record(p.role, provider, model, time.Since(start), err)
		return resp, err
	}
	switch rr.mode {
	case "offline":
		return finish(Response{}, ErrNoProvider)
	case "api":
		if rr.api == nil {
			return finish(Response{}, ErrNoProvider)
		}
		resp, err := rr.api.Complete(ctx, req)
		return finish(resp, err)
	case "local":
		if rr.local == nil {
			return finish(Response{}, ErrNoProvider)
		}
		resp, err := rr.local.Complete(ctx, req)
		return finish(resp, err)
	default:
		for _, local := range rr.localChain() {
			if !local.Available(ctx) {
				continue
			}
			if resp, err := local.Complete(ctx, req); err == nil {
				return finish(resp, nil)
			}
		}
		if rr.api != nil && rr.api.Available(ctx) {
			resp, err := rr.api.Complete(ctx, req)
			return finish(resp, err)
		}
		return finish(Response{}, ErrNoProvider)
	}
}

// record stores the latest call evidence for one role. It is intentionally
// cheap: one mutex acquisition per LLM call, no allocation on the hot path
// beyond the map lookup.
func (r *Router) record(role, provider, model string, latency time.Duration, err error) {
	r.statMu.Lock()
	defer r.statMu.Unlock()
	if r.stats == nil {
		r.stats = make(map[string]*roleStat)
	}
	st := r.stats[role]
	if st == nil {
		st = &roleStat{}
		r.stats[role] = st
	}
	st.Calls++
	st.LastAt = time.Now()
	st.LastLatency = latency
	if provider != "" {
		st.LastProv = provider
	}
	if model != "" {
		st.LastModel = model
	}
	if err != nil {
		st.Errors++
		st.LastError = err.Error()
	} else {
		st.LastError = ""
	}
}

func (r *Router) roleStats() map[string]roleStat {
	r.statMu.Lock()
	defer r.statMu.Unlock()
	out := make(map[string]roleStat, len(r.stats))
	for role, st := range r.stats {
		out[role] = *st
	}
	return out
}

// RoutesStatus reports the effective provider/model for every role. It is
// meant for GET /api/ai/routes and the llm settings page.
func (r *Router) RoutesStatus(ctx context.Context) map[string]any {
	configured := r.Routes()
	roles := Roles()
	for role := range configured {
		found := false
		for _, known := range roles {
			if known == role {
				found = true
				break
			}
		}
		if !found {
			roles = append(roles, role)
		}
	}

	out := make(map[string]any, len(roles))
	stats := r.roleStats()
	for _, role := range roles {
		rr := r.resolveRoute(role)
		entry := map[string]any{
			"mode":        rr.mode,
			"model":       rr.model,
			"available":   false,
			"provider":    "",
			"implemented": role == RolePerception || role == RoleAnalysis || role == RoleStrategy || role == RoleLanguage,
			"calls":       int64(0),
			"errors":      int64(0),
			"state":       "idle",
		}
		if !entry["implemented"].(bool) {
			entry["state"] = "not_wired"
		}
		if st, ok := stats[role]; ok {
			entry["calls"] = st.Calls
			entry["errors"] = st.Errors
			if st.LastModel != "" {
				entry["model"] = st.LastModel
			}
			if st.LastProv != "" {
				entry["provider"] = st.LastProv
			}
			if !st.LastAt.IsZero() {
				entry["last_at"] = st.LastAt.Format(time.RFC3339)
			}
			entry["last_latency_ms"] = st.LastLatency.Milliseconds()
			if st.LastError != "" {
				entry["last_error"] = st.LastError
				entry["state"] = "error"
			} else if entry["implemented"].(bool) {
				entry["state"] = "working"
			}
		}
		if rc, ok := configured[role]; ok {
			if rc.Mode != "" {
				entry["configured_mode"] = rc.Mode
			}
			if rc.Model != "" {
				entry["configured_model"] = rc.Model
			}
		}
		var chosen Provider
		switch rr.mode {
		case "api":
			chosen = rr.api
		case "local":
			chosen = rr.local
		case "auto":
			if local := rr.availableLocal(ctx); local != nil {
				chosen = local
			} else {
				chosen = rr.api
			}
		}
		if chosen != nil {
			entry["provider"] = chosen.Name()
			if mp, ok := chosen.(ModelProvider); ok {
				if model := strings.TrimSpace(mp.Model()); model != "" {
					entry["model"] = model
				}
			}
			entry["available"] = chosen.Available(ctx)
		}
		note := rr.reason()
		if !entry["implemented"].(bool) {
			if note != "" {
				note += "; 该层尚未接入调用点，选择会被保存，但当前由规则层处理"
			} else {
				note = "该层尚未接入调用点，选择会被保存，但当前由规则层处理"
			}
		}
		if note != "" {
			entry["note"] = note
		}
		if !entry["available"].(bool) && entry["note"] == nil {
			entry["note"] = rr.reason()
		}
		if !entry["available"].(bool) && entry["state"] != "error" && entry["state"] != "not_wired" {
			entry["state"] = "unavailable"
		}
		out[role] = entry
	}
	return out
}

// Mode returns the current routing mode.
func (r *Router) Mode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mode
}

// Providers returns the current provider pair.
func (r *Router) Providers() (Provider, Provider) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.api, r.local
}

func (r *Router) snapshot() (string, Provider, Provider, map[string]Provider) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mode, r.api, r.local, r.locals
}

func (r *Router) Name() string { return "router:" + r.Mode() }

// Available implements Provider so a Router can be injected anywhere a single
// provider is expected (the policy layer uses this).
func (r *Router) Available(ctx context.Context) bool {
	mode, api, local, _ := r.snapshot()
	switch mode {
	case "offline":
		return false
	case "api":
		return api != nil && api.Available(ctx)
	case "local":
		return local != nil && local.Available(ctx)
	default:
		if local != nil && local.Available(ctx) {
			return true
		}
		return api != nil && api.Available(ctx)
	}
}

func (r *Router) Status(ctx context.Context) map[string]any {
	mode, api, local, locals := r.snapshot()
	out := map[string]any{"mode": mode}
	if api != nil {
		out["api"] = api.Name()
		out["api_available"] = api.Available(ctx)
	}
	if local != nil {
		out["local"] = local.Name()
		out["local_available"] = local.Available(ctx)
	}
	if len(locals) > 0 {
		models := make(map[string]string, len(locals))
		for id, p := range locals {
			models[id] = p.Name()
		}
		out["local_models"] = models
	}
	out["routes"] = r.RoutesStatus(ctx)
	return out
}

func (r *Router) Complete(ctx context.Context, req Request) (Response, error) {
	mode, api, local, _ := r.snapshot()
	switch mode {
	case "offline":
		return Response{}, ErrNoProvider
	case "api":
		if api == nil {
			return Response{}, ErrNoProvider
		}
		return api.Complete(ctx, req)
	case "local":
		if local == nil {
			return Response{}, ErrNoProvider
		}
		return local.Complete(ctx, req)
	default: // auto: prefer local (privacy, no API key), fall back to API
		if local != nil && local.Available(ctx) {
			if resp, err := local.Complete(ctx, req); err == nil {
				return resp, nil
			}
		}
		if api != nil && api.Available(ctx) {
			return api.Complete(ctx, req)
		}
		return Response{}, ErrNoProvider
	}
}

// ExtractJSON returns the first balanced JSON object in text, tolerating
// markdown fences and leading prose from small models.
func ExtractJSON(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		s = strings.TrimPrefix(s, "JSON")
		s = strings.TrimSpace(s)
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	start := strings.Index(s, "{")
	if start < 0 {
		start = strings.Index(s, "[")
		if start < 0 {
			return "", false
		}
	}
	if s[start] == '[' {
		depth := 0
		inStr := false
		esc := false
		for i := start; i < len(s); i++ {
			c := s[i]
			if inStr {
				if esc {
					esc = false
				} else if c == '\\' {
					esc = true
				} else if c == '"' {
					inStr = false
				}
				continue
			}
			switch c {
			case '"':
				inStr = true
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					return s[start : i+1], true
				}
			}
		}
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
