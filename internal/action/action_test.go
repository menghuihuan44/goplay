package action

import (
	"context"
	"testing"

	"goplay/internal/domain"
	"goplay/internal/mockgame"
)

func TestMockExecutorDecodesKey(t *testing.T) {
	g := mockgame.New(3)
	e := &MockExecutor{Game: g}
	a := domain.Action{Kind: "attack", Target: "enemy", Input: &domain.InputSpec{Type: "key", Key: "a"}}
	if err := e.Do(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	gt := g.GroundTruth()
	if gt.Values["enemy_hp"] >= gt.Values["enemy_max_hp"] {
		t.Fatal("attack did not change enemy hp")
	}
}

func TestHumanBlocksAction(t *testing.T) {
	g := mockgame.New(3)
	h := NewMockHuman()
	e := &MockExecutor{Game: g, Human: h}
	h.Touch()
	a := domain.Action{Kind: "attack", Input: &domain.InputSpec{Type: "key", Key: "a"}}
	if err := e.Do(context.Background(), a); err != ErrHumanControl {
		t.Fatalf("want ErrHumanControl, got %v", err)
	}
}

func TestLimiterPaces(t *testing.T) {
	l := NewLimiter(1000)
	for i := 0; i < 3; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
