package perception

import (
	"context"
	"math"
	"testing"

	"goplay/internal/mockgame"
)

func TestMockRoundTrip(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		g := mockgame.New(seed)
		src := NewMockSource(g, 0)
		an := MockAnalyzer{}
		for step := 0; step < 80; step++ {
			actions := []string{"attack", "defend", "heal", "advance", "rest", "attack", "attack"}
			_ = g.Apply(actions[step%len(actions)])
			f, err := src.Grab(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			st, _, err := an.Analyze(f)
			if err != nil {
				t.Fatal(err)
			}
			gt := g.GroundTruth()
			if math.Abs(st.Values["hp"]-gt.Values["hp"]) > 1.5 {
				t.Fatalf("seed %d step %d hp: got %.1f want %.0f", seed, step, st.Values["hp"], gt.Values["hp"])
			}
			if math.Abs(st.Values["room"]-gt.Values["room"]) > 0.01 {
				t.Fatalf("seed %d step %d room: got %.0f want %.0f", seed, step, st.Values["room"], gt.Values["room"])
			}
			if math.Abs(st.Values["enemy_hp"]-gt.Values["enemy_hp"]) > 1.5 {
				t.Fatalf("seed %d step %d enemy: got %.1f want %.0f", seed, step, st.Values["enemy_hp"], gt.Values["enemy_hp"])
			}
			if math.Abs(st.Values["potions"]-gt.Values["potions"]) > 0.01 {
				t.Fatalf("seed %d step %d potions: got %.0f want %.0f", seed, step, st.Values["potions"], gt.Values["potions"])
			}
		}
	}
}

func TestMockRoundTripWithNoise(t *testing.T) {
	g := mockgame.New(42)
	src := NewMockSource(g, 0.005)
	an := MockAnalyzer{}
	for step := 0; step < 200; step++ {
		if step%3 == 0 {
			_ = g.Apply("attack")
		}
		f, err := src.Grab(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		st, _, err := an.Analyze(f)
		if err != nil {
			t.Fatal(err)
		}
		gt := g.GroundTruth()
		if math.Abs(st.Values["hp"]-gt.Values["hp"]) > 1.5 {
			t.Fatalf("step %d hp mismatch: %.1f vs %.0f", step, st.Values["hp"], gt.Values["hp"])
		}
		if st.Values["room"] != gt.Values["room"] {
			t.Fatalf("step %d room mismatch: %.0f vs %.0f", step, st.Values["room"], gt.Values["room"])
		}
	}
}

func TestGenericAnalyzer(t *testing.T) {
	g := mockgame.New(7)
	f := mockgame.Render(g)
	st, world, err := GenericAnalyzer{}.Analyze(f)
	if err != nil {
		t.Fatal(err)
	}
	if st.Values["width"] != 320 || world["height"] != 180 {
		t.Fatalf("unexpected analysis: %+v %+v", st, world)
	}
}
