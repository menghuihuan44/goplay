// Package mockgame implements a deterministic dungeon used to train and
// evaluate the agent. The simulator deliberately exposes no internal state to
// the agent: the only channel out of it is a rendered RGBA frame, and the only
// channel in is a semantic action string. This keeps the training loop honest.
package mockgame

import (
	"fmt"
	"math/rand"
	"sync"

	"goplay/internal/domain"
)

const (
	MaxRooms = 10
	W        = 320
	H        = 180
)

type Game struct {
	mu    sync.Mutex
	rng   *rand.Rand
	seed  int64
	HP    int
	MaxHP int
	// Potions is the number of healing potions left.
	Potions int
	Room    int
	Gold    int

	EnemyHP    int
	EnemyMaxHP int
	Boss       bool

	Dead  bool
	Won   bool
	Steps int
	Log   []string
}

func New(seed int64) *Game {
	g := &Game{seed: seed}
	g.Reset()
	return g
}

func (g *Game) Seed() int64 { return g.seed }

func (g *Game) Reset() {
	g.rng = rand.New(rand.NewSource(g.seed))
	g.MaxHP = 100
	g.HP = g.MaxHP
	g.Potions = 4
	g.Room = 1
	g.Gold = 0
	g.Dead = false
	g.Won = false
	g.Steps = 0
	g.Log = nil
	g.spawn()
}

func (g *Game) spawn() {
	g.Boss = g.Room >= MaxRooms
	if g.Boss {
		g.EnemyMaxHP = 90
	} else {
		g.EnemyMaxHP = 18 + g.Room*5
	}
	g.EnemyHP = g.EnemyMaxHP
}

// Progress is the coarse learning signal used by the evaluator: rooms cleared
// plus partial progress toward the current room's objective.
func (g *Game) Progress() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := float64(g.Room-1) / float64(MaxRooms)
	if g.EnemyMaxHP > 0 {
		p += (1.0 - float64(g.EnemyHP)/float64(g.EnemyMaxHP)) / float64(MaxRooms)
	}
	if g.Won {
		p = 1
	}
	if p > 1 {
		p = 1
	}
	return p
}

func (g *Game) Snapshot() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return map[string]any{
		"hp": g.HP, "max_hp": g.MaxHP,
		"enemy_hp": g.EnemyHP, "enemy_max_hp": g.EnemyMaxHP,
		"potions": g.Potions, "room": g.Room, "gold": g.Gold,
		"dead": g.Dead, "won": g.Won, "boss": g.Boss,
		"steps": g.Steps,
	}
}

func (g *Game) EnemyAlive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.EnemyHP > 0 && !g.Dead && !g.Won
}

func (g *Game) logf(format string, args ...any) {
	g.Log = append(g.Log, fmt.Sprintf(format, args...))
	if len(g.Log) > 200 {
		g.Log = g.Log[len(g.Log)-200:]
	}
}

func (g *Game) enemyDamage() int {
	if g.Boss {
		return 5 + g.rng.Intn(5)
	}
	return 2 + g.rng.Intn(3)
}

func (g *Game) enemyStrike() {
	dmg := g.enemyDamage()
	g.HP -= dmg
	g.logf("enemy hits for %d", dmg)
	if g.HP <= 0 {
		g.HP = 0
		g.Dead = true
		g.logf("you died")
	}
}

// Apply performs one semantic action. Unknown actions are rejected without
// changing state, which lets the validator and the learner detect mistakes.
func (g *Game) Apply(kind string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Dead || g.Won {
		return fmt.Errorf("game over")
	}
	g.Steps++
	switch kind {
	case "attack":
		if g.EnemyHP <= 0 {
			g.logf("attack wasted: room already clear")
			return nil
		}
		dmg := 7 + g.rng.Intn(8)
		if g.Boss && g.rng.Intn(5) == 0 {
			dmg += 6
			g.logf("critical hit")
		}
		g.EnemyHP -= dmg
		if g.EnemyHP < 0 {
			g.EnemyHP = 0
		}
		g.logf("you hit for %d (enemy %d)", dmg, g.EnemyHP)
		if g.EnemyHP == 0 {
			reward := 8 + g.Room
			g.Gold += reward
			g.logf("enemy defeated, +%d gold", reward)
		} else {
			g.enemyStrike()
		}
	case "defend":
		g.logf("you defend")
		if g.EnemyHP > 0 {
			dmg := g.enemyDamage() / 4
			if dmg < 1 {
				dmg = 1
			}
			g.HP -= dmg
			if g.HP <= 0 {
				g.HP = 0
				g.Dead = true
				g.logf("you died while defending")
			}
		}
	case "heal":
		if g.Potions <= 0 {
			g.logf("no potions left")
			return nil
		}
		g.Potions--
		g.HP += 28
		if g.HP > g.MaxHP {
			g.HP = g.MaxHP
		}
		g.logf("potion used (+28 hp)")
		if g.EnemyHP > 0 {
			g.enemyStrike()
		}
	case "rest":
		if g.EnemyHP > 0 {
			g.logf("cannot rest with enemy alive")
			g.enemyStrike()
			return nil
		}
		g.HP += 8
		if g.HP > g.MaxHP {
			g.HP = g.MaxHP
		}
		g.logf("rested (+8 hp)")
	case "advance":
		if g.EnemyHP > 0 {
			g.logf("advance blocked: enemy still alive")
			g.enemyStrike()
			return nil
		}
		if g.Room >= MaxRooms {
			g.Won = true
			g.logf("dungeon cleared")
			return nil
		}
		g.Room++
		g.spawn()
		g.logf("entered room %d", g.Room)
	case "wait", "noop":
		g.logf("wait")
		if g.EnemyHP > 0 {
			g.enemyStrike()
		}
	default:
		g.Steps--
		return fmt.Errorf("unknown action %q", kind)
	}
	return nil
}

// GroundTruth is exposed only to the evaluator, never to the agent.
func (g *Game) GroundTruth() domain.State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return domain.State{
		Summary: fmt.Sprintf("room %d hp %d enemy %d", g.Room, g.HP, g.EnemyHP),
		Values: map[string]float64{
			"hp": float64(g.HP), "max_hp": float64(g.MaxHP),
			"enemy_hp": float64(g.EnemyHP), "enemy_max_hp": float64(g.EnemyMaxHP),
			"room": float64(g.Room), "potions": float64(g.Potions),
			"gold": float64(g.Gold),
		},
		Text: map[string]string{
			"dead": fmt.Sprint(g.Dead), "won": fmt.Sprint(g.Won),
		},
		Confidence: 1,
	}
}
