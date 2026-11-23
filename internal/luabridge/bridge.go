package luabridge

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"

	"goplay/internal/domain"
)

//go:embed profiles/*.lua
var profileFS embed.FS

type Profile struct {
	ID          string
	Name        string
	Description string
	Games       []string
	Rules       domain.InstanceRules
	Source      string
	L           *lua.LState
	tbl         *lua.LTable
	closed      bool
}

type Interpretation struct {
	Reward   float64
	Delta    float64
	Terminal bool
	Won      bool
	Note     string
}

var (
	loadMu sync.Mutex
)

// BuiltinIDs lists embedded Lua profiles.
func BuiltinIDs() []string {
	entries, err := profileFS.ReadDir("profiles")
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lua") {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".lua"))
		}
	}
	sort.Strings(ids)
	return ids
}

func LoadBuiltin(id string) (*Profile, error) {
	if id == "" {
		id = "generic"
	}
	b, err := profileFS.ReadFile("profiles/" + id + ".lua")
	if err != nil {
		return nil, fmt.Errorf("unknown Lua profile %q: %w", id, err)
	}
	return LoadSource(id, string(b))
}

// LoadSource creates an isolated Lua state for one instance. Lua states are
// not goroutine-safe, so every game instance owns its own profile object.
func LoadSource(id, source string) (*Profile, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	L := lua.NewState(lua.Options{SkipOpenLibs: false})
	sandbox(L)
	fn, err := L.LoadString(source)
	if err != nil {
		L.Close()
		return nil, fmt.Errorf("load lua %s: %w", id, err)
	}
	L.Push(fn)
	if err := L.PCall(0, 1, nil); err != nil {
		L.Close()
		return nil, fmt.Errorf("run lua %s: %w", id, err)
	}
	ret := L.Get(-1)
	L.Pop(1)
	tbl, ok := ret.(*lua.LTable)
	if !ok {
		L.Close()
		return nil, fmt.Errorf("lua profile %s must return a table", id)
	}
	p := &Profile{ID: id, Source: source, L: L, tbl: tbl}
	p.ID = firstNonEmpty(asString(tableField(tbl, "id")), id)
	p.Name = firstNonEmpty(asString(tableField(tbl, "name")), id)
	p.Description = asString(tableField(tbl, "description"))
	p.Games = stringSlice(tableField(tbl, "games"))
	if rules, ok := tableField(tbl, "rules").(*lua.LTable); ok {
		p.Rules.RequiredTags = stringSlice(tableField(rules, "required_tags"))
		p.Rules.ForbiddenTags = stringSlice(tableField(rules, "forbidden_tags"))
		p.Rules.Notes = stringSlice(tableField(rules, "notes"))
	}
	p.Rules.RequiredTags = append(p.Rules.RequiredTags, stringSlice(tableField(tbl, "required_tags"))...)
	p.Rules.ForbiddenTags = append(p.Rules.ForbiddenTags, stringSlice(tableField(tbl, "forbidden_tags"))...)
	return p, nil
}

// sandbox removes libraries that could touch the host filesystem, processes or
// debug hooks. Profiles only need base/table/string/math; keeping the game
// semantics declarative is also what makes them safe to load per instance.
func sandbox(L *lua.LState) {
	for _, name := range []string{"os", "io", "debug", "package", "channel"} {
		L.SetGlobal(name, lua.LNil)
	}
	for _, name := range []string{"dofile", "loadfile", "require"} {
		L.SetGlobal(name, lua.LNil)
	}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func (p *Profile) Close() {
	if p == nil || p.L == nil {
		return
	}
	p.closed = true
	p.L.Close()
}

func (p *Profile) Info() domain.GameProfileInfo {
	return domain.GameProfileInfo{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Games:       append([]string(nil), p.Games...),
		Source:      "builtin",
	}
}

// worldTable flattens the world snapshot and attaches the perception state.
func (p *Profile) worldTable(world map[string]any, st domain.State) *lua.LTable {
	t := p.L.NewTable()
	for k, v := range world {
		t.RawSetString(k, toLua(p.L, v))
	}
	t.RawSetString("values", toLua(p.L, map[string]float64(st.Values)))
	t.RawSetString("text", toLua(p.L, map[string]string(st.Text)))
	t.RawSetString("tags", toLua(p.L, st.Tags))
	t.RawSetString("summary", lua.LString(st.Summary))
	t.RawSetString("confidence", lua.LNumber(st.Confidence))
	return t
}

func (p *Profile) call(name string, args ...lua.LValue) ([]lua.LValue, error) {
	fn := p.L.GetField(p.tbl, name)
	if fn == lua.LNil {
		return nil, fmt.Errorf("lua profile %s: missing function %s", p.ID, name)
	}
	p.L.Push(fn)
	for _, a := range args {
		p.L.Push(a)
	}
	if err := p.L.PCall(len(args), lua.MultRet, nil); err != nil {
		p.L.Pop(1) // error
		return nil, fmt.Errorf("lua profile %s.%s: %w", p.ID, name, err)
	}
	n := p.L.GetTop() - 0
	// The return values are on top; the function was consumed by PCall.
	results := make([]lua.LValue, n)
	for i := 0; i < n; i++ {
		results[i] = p.L.Get(i + 1)
	}
	p.L.SetTop(0)
	return results, nil
}

// Observe lets the Lua profile turn raw world data into the decision state.
func (p *Profile) Observe(world map[string]any, st domain.State) (domain.State, error) {
	res, err := p.call("observe", p.worldTable(world, st))
	if err != nil {
		return st, err
	}
	out := domain.State{Values: map[string]float64{}, Text: map[string]string{}, At: st.At, Raw: world, Confidence: 1}
	if len(res) > 0 {
		if tbl, ok := res[0].(*lua.LTable); ok {
			out.Summary = asString(tableField(tbl, "summary"))
			if vals, ok := tableField(tbl, "values").(*lua.LTable); ok {
				vals.ForEach(func(k, v lua.LValue) {
					if s := asString(k); s != "" {
						out.Values[s] = asFloat(v)
					}
				})
			}
			if vals, ok := tableField(tbl, "text").(*lua.LTable); ok {
				vals.ForEach(func(k, v lua.LValue) {
					if s := asString(k); s != "" {
						out.Text[s] = asString(v)
					}
				})
			}
			out.Tags = stringSlice(tableField(tbl, "tags"))
			if c := asFloat(tableField(tbl, "confidence")); c > 0 {
				out.Confidence = c
			}
		}
	}
	if out.Summary == "" {
		out.Summary = st.Summary
	}
	return out, nil
}

// Actions returns the game's legal semantic actions for the current state.
func (p *Profile) Actions(world map[string]any, st domain.State) ([]domain.Action, error) {
	res, err := p.call("actions", p.worldTable(world, st))
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, nil
	}
	tbl, ok := res[0].(*lua.LTable)
	if !ok {
		return nil, nil
	}
	var out []domain.Action
	tbl.ForEach(func(_, v lua.LValue) {
		at, ok := v.(*lua.LTable)
		if !ok {
			return
		}
		a := domain.Action{
			Kind:   asString(tableField(at, "kind")),
			Target: asString(tableField(at, "target")),
			Reason: asString(tableField(at, "reason")),
			Tags:   stringSlice(tableField(at, "tags")),
			Risk:   asFloat(tableField(at, "risk")),
		}
		if a.Kind == "" {
			return
		}
		if m, ok := fromLua(tableField(at, "params")).(map[string]any); ok {
			a.Params = m
		}
		if it, ok := tableField(at, "input").(*lua.LTable); ok {
			a.Input = &domain.InputSpec{
				Type:       asString(tableField(it, "type")),
				Key:        asString(tableField(it, "key")),
				Text:       asString(tableField(it, "text")),
				X:          asFloat(tableField(it, "x")),
				Y:          asFloat(tableField(it, "y")),
				X2:         asFloat(tableField(it, "x2")),
				Y2:         asFloat(tableField(it, "y2")),
				Button:     asString(tableField(it, "button")),
				DurationMS: int(asFloat(tableField(it, "duration_ms"))),
				ClickCount: int(asFloat(tableField(it, "click_count"))),
			}
		}
		out = append(out, a)
	})
	return out, nil
}

func (p *Profile) Interpret(before, after map[string]any) (Interpretation, error) {
	res, err := p.call("interpret", toLua(p.L, before), toLua(p.L, after))
	if err != nil {
		return Interpretation{}, err
	}
	var out Interpretation
	if len(res) > 0 {
		if tbl, ok := res[0].(*lua.LTable); ok {
			out.Reward = asFloat(tableField(tbl, "reward"))
			out.Delta = asFloat(tableField(tbl, "delta"))
			out.Terminal = asBool(tableField(tbl, "terminal"))
			out.Won = asBool(tableField(tbl, "won"))
			out.Note = asString(tableField(tbl, "note"))
		}
	}
	return out, nil
}
