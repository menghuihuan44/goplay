package luabridge

import (
	"fmt"
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

func TestGenericTouchInterpretsFlatSceneChange(t *testing.T) {
	p, err := LoadBuiltin("generic_touch")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// The engine passes the flat world map, not {"values": {...}}.
	before := map[string]any{"frame_delta": 0.0, "scene_change": 0.0}
	after := map[string]any{"frame_delta": 0.06, "scene_change": 0.5}
	in, err := p.Interpret(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if in.Reward < 0.05 {
		t.Fatalf("touch scene change must be effective, got %+v", in)
	}
	if in.Note == "no meaningful touch change" {
		t.Fatalf("touch change was not reported: %+v", in)
	}
}

func TestGenericTouchTapsOCRLabels(t *testing.T) {
	p, err := LoadBuiltin("generic_touch")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	world := map[string]any{
		"ui_regions": []any{
			map[string]any{"text": "开始游戏", "x": 0.5, "y": 0.62, "w": 0.2, "h": 0.06},
			map[string]any{"text": "继续游戏", "x": 0.5, "y": 0.4, "w": 0.2, "h": 0.06},
		},
	}
	st, err := p.Observe(world, domain.State{Values: map[string]float64{}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Values["ui_count"] != 2 {
		t.Fatalf("OCR count not folded into state: %+v", st.Values)
	}
	acts, err := p.Actions(world, st)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if a.Kind == "tap_text" && a.Input != nil && a.Input.Type == "tap" {
			if math.Abs(a.Input.Y-0.62) < 0.001 || math.Abs(a.Input.Y-0.4) < 0.001 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected a tap_text action on an OCR box, got %+v", acts)
	}
}

func TestGenericTouchTapsLabelsBeyondTwelve(t *testing.T) {
	p, err := LoadBuiltin("generic_touch")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	regions := make([]any, 0, 14)
	for i := 1; i <= 12; i++ {
		regions = append(regions, map[string]any{
			"text": fmt.Sprintf("HUD %d", i), "x": 0.1, "y": 0.1, "w": 0.1, "h": 0.03,
		})
	}
	regions = append(regions,
		map[string]any{"text": "再玩一会", "x": 0.5, "y": 0.55, "w": 0.2, "h": 0.06},
		map[string]any{"text": "退出游戏", "x": 0.5, "y": 0.72, "w": 0.2, "h": 0.06},
	)
	world := map[string]any{"ui_regions": regions}
	st, err := p.Observe(world, domain.State{Values: map[string]float64{}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Values["ui_count"] != 14 {
		t.Fatalf("expected all 14 OCR boxes, got %+v", st.Values["ui_count"])
	}
	acts, err := p.Actions(world, st)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"text:再玩一会": false, "text:退出游戏": false}
	for _, a := range acts {
		if a.Kind == "tap_text" {
			if _, ok := want[a.Target]; ok {
				want[a.Target] = true
			}
		}
	}
	for label, found := range want {
		if !found {
			t.Fatalf("expected %s action beyond the first 12 OCR boxes, got %+v", label, acts)
		}
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
