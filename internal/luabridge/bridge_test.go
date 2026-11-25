package luabridge

import (
	"math"
	"testing"

	"goplay/internal/domain"
)

func TestBuiltinProfilesLoad(t *testing.T) {
	ids := BuiltinIDs()
	if len(ids) < 2 {
		t.Fatalf("expected at least 2 builtin profiles, got %v", ids)
	}
	for _, id := range ids {
		p, err := LoadBuiltin(id)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if p.ID == "" || p.Name == "" {
			t.Fatalf("profile %s missing metadata: %+v", id, p.Info())
		}
		p.Close()
	}
}

func TestProfilesRunWithoutHostLibraries(t *testing.T) {
	src := `if os ~= nil or io ~= nil or debug ~= nil or package ~= nil or dofile ~= nil or loadfile ~= nil or require ~= nil then
  error("host library leaked into profile sandbox")
end
return { id = "sandbox", name = "sandbox" }`
	p, err := LoadSource("sandbox", src)
	if err != nil {
		t.Fatalf("sandboxed profile should load: %v", err)
	}
	p.Close()
}

func TestMockDungeonObserveActionsInterpret(t *testing.T) {
	p, err := LoadBuiltin("mock_dungeon")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	world := map[string]any{
		"hp": 100, "max_hp": 100, "enemy_hp": 30, "enemy_max_hp": 30,
		"potions": 1, "room": 2, "gold": 0, "dead": false, "won": false,
	}
	st, err := p.Observe(world, domain.State{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Values["hp"] != 100 || st.Text["phase"] != "combat" {
		t.Fatalf("unexpected state: %+v", st)
	}
	actions, err := p.Actions(world, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) < 2 {
		t.Fatalf("expected attack/defend, got %+v", actions)
	}
	if actions[0].Kind != "attack" || actions[0].Input == nil || actions[0].Input.Key != "a" {
		t.Fatalf("unexpected first action: %+v", actions[0])
	}

	before := map[string]any{"hp": 100, "enemy_hp": 30, "room": 2}
	after := map[string]any{"hp": 90, "enemy_hp": 10, "room": 3, "won": false, "dead": false}
	in, err := p.Interpret(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if in.Delta != 1 || in.Terminal || math.Abs(in.Reward-10.4) > 0.2 {
		t.Fatalf("unexpected interpretation: %+v", in)
	}
}

func TestGenericInterpretsFlatSceneChange(t *testing.T) {
	p, err := LoadBuiltin("generic")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	before := map[string]any{"frame_delta": 0.0, "scene_change": 0.0}
	after := map[string]any{"frame_delta": 0.04, "scene_change": 0.44}
	in, err := p.Interpret(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if in.Reward < 0.05 {
		t.Fatalf("a visible scene change must count as effective, got %+v", in)
	}
	if in.Note == "no meaningful change" {
		t.Fatalf("scene change was not reported: %+v", in)
	}
}

func TestNoActionsWhenTerminal(t *testing.T) {
	p, err := LoadBuiltin("mock_dungeon")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	world := map[string]any{"hp": 0, "max_hp": 100, "enemy_hp": 0, "room": 10, "dead": true, "won": false}
	st, err := p.Observe(world, domain.State{})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := p.Actions(world, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 0 {
		t.Fatalf("dead game should have no actions, got %+v", actions)
	}
}
