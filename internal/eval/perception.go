package eval

import (
	"context"
	"fmt"
	"math"
	"time"

	"goplay/internal/domain"
	"goplay/internal/mockgame"
	"goplay/internal/perception"
)

// noiseLevel is one renderer-noise condition for the perception suite.
type noiseLevel struct {
	Name  string
	Noise float64
}

var defaultNoiseLevels = []noiseLevel{
	{Name: "clean", Noise: 0},
	{Name: "low", Noise: 0.001},
	{Name: "mid", Noise: 0.005},
	{Name: "high", Noise: 0.010},
}

type perceptionCounts struct {
	Samples int `json:"samples"`
	HP      int `json:"hp_correct"`
	EnemyHP int `json:"enemy_hp_correct"`
	Room    int `json:"room_correct"`
	Potions int `json:"potions_correct"`
	Dead    int `json:"dead_correct"`
	Won     int `json:"won_correct"`
}

func (c *perceptionCounts) totalCorrect() int {
	return c.HP + c.EnemyHP + c.Room + c.Potions
}

func (c *perceptionCounts) totalFields() int { return c.Samples * 4 }

func (c *perceptionCounts) ratio(n int) float64 {
	if c.Samples == 0 {
		return 0
	}
	return float64(n) / float64(c.Samples)
}

func stateValue(st domain.State, key string, def float64) float64 {
	if st.Values == nil {
		return def
	}
	if v, ok := st.Values[key]; ok {
		return v
	}
	return def
}

// perceptionSeed runs a deterministic mock game and compares the mock
// analyzer with GroundTruth after every step. The action policy is deliberately
// simple and identical for all seeds and noise levels.
func perceptionSeed(seed int64, noise float64, steps int) (perceptionCounts, error) {
	g := mockgame.New(seed)
	src := perception.NewMockSource(g, noise)
	an := perception.MockAnalyzer{}
	var c perceptionCounts
	for i := 0; i < steps; i++ {
		if g.Dead || g.Won {
			g.Reset()
		}
		var kind string
		switch {
		case g.HP <= 40 && g.Potions > 0:
			kind = "heal"
		case g.EnemyAlive():
			kind = "attack"
		default:
			kind = "advance"
		}
		if err := g.Apply(kind); err != nil {
			return c, fmt.Errorf("seed %d step %d apply %s: %w", seed, i, kind, err)
		}
		f, err := src.Grab(context.Background())
		if err != nil {
			return c, err
		}
		est, _, err := an.Analyze(f)
		if err != nil {
			return c, err
		}
		truth := g.GroundTruth()
		c.Samples++
		if math.Abs(stateValue(est, "hp", -1)-stateValue(truth, "hp", -2)) <= 1.0 {
			c.HP++
		}
		if math.Abs(stateValue(est, "enemy_hp", -1)-stateValue(truth, "enemy_hp", -2)) <= 1.0 {
			c.EnemyHP++
		}
		if stateValue(est, "room", -1) == stateValue(truth, "room", -2) {
			c.Room++
		}
		if stateValue(est, "potions", -1) == stateValue(truth, "potions", -2) {
			c.Potions++
		}
		if (stateValue(est, "dead", -1) > 0.5) == (stateValue(truth, "dead", -2) > 0.5) {
			c.Dead++
		}
		if (stateValue(est, "won", -1) > 0.5) == (stateValue(truth, "won", -2) > 0.5) {
			c.Won++
		}
	}
	return c, nil
}

type perceptionLevelResult struct {
	Noise           float64          `json:"noise"`
	Counts          perceptionCounts `json:"counts"`
	HPAccuracy      float64          `json:"hp_accuracy"`
	EnemyHPAccuracy float64          `json:"enemy_hp_accuracy"`
	RoomAccuracy    float64          `json:"room_accuracy"`
	PotionsAccuracy float64          `json:"potions_accuracy"`
	OverallAccuracy float64          `json:"overall_accuracy"`
}

func runPerception(ctx context.Context, cfg evalConfig) (s Suite) {
	s = Suite{ID: "perception", Title: "Perception accuracy under renderer noise"}
	start := time.Now()
	defer func() { s.DurationMS = time.Since(start).Milliseconds() }()

	var results []perceptionLevelResult
	byName := map[string]perceptionLevelResult{}
	for _, lv := range defaultNoiseLevels {
		lr := perceptionLevelResult{Noise: lv.Noise}
		for _, seed := range cfg.perceptionSeeds {
			if err := ctx.Err(); err != nil {
				s.Error = err.Error()
				s.Finalize()
				return s
			}
			c, err := perceptionSeed(seed, lv.Noise, cfg.perceptionSteps)
			if err != nil {
				s.Error = err.Error()
				s.Finalize()
				return s
			}
			lr.Counts.Samples += c.Samples
			lr.Counts.HP += c.HP
			lr.Counts.EnemyHP += c.EnemyHP
			lr.Counts.Room += c.Room
			lr.Counts.Potions += c.Potions
			lr.Counts.Dead += c.Dead
			lr.Counts.Won += c.Won
		}
		if lr.Counts.Samples > 0 {
			lr.HPAccuracy = lr.Counts.ratio(lr.Counts.HP)
			lr.EnemyHPAccuracy = lr.Counts.ratio(lr.Counts.EnemyHP)
			lr.RoomAccuracy = lr.Counts.ratio(lr.Counts.Room)
			lr.PotionsAccuracy = lr.Counts.ratio(lr.Counts.Potions)
			lr.OverallAccuracy = float64(lr.Counts.totalCorrect()) / float64(lr.Counts.totalFields())
		}
		results = append(results, lr)
		byName[lv.Name] = lr
	}

	for i, lr := range results {
		name := fmt.Sprintf("noise_%.3f", lr.Noise)
		s.Add(fmt.Sprintf("perception.%s.hp_accuracy", name), fmt.Sprintf("HP accuracy @ level %d", i), lr.HPAccuracy, "ratio", "")
		s.Add(fmt.Sprintf("perception.%s.enemy_hp_accuracy", name), fmt.Sprintf("enemy HP accuracy @ level %d", i), lr.EnemyHPAccuracy, "ratio", "")
		s.Add(fmt.Sprintf("perception.%s.room_accuracy", name), fmt.Sprintf("room accuracy @ level %d", i), lr.RoomAccuracy, "ratio", "")
		s.Add(fmt.Sprintf("perception.%s.potions_accuracy", name), fmt.Sprintf("potions accuracy @ level %d", i), lr.PotionsAccuracy, "ratio", "")
	}
	if p, ok := byName["clean"]; ok {
		s.Check("perception.clean_overall_accuracy", "overall field accuracy on clean frames", p.OverallAccuracy, ">=", 0.99, "ratio", "HP/room/enemy/potions; HP tolerance 1.0")
	}
	if p, ok := byName["low"]; ok {
		s.Check("perception.low_noise_overall_accuracy", "overall field accuracy at 0.1% pixel noise", p.OverallAccuracy, ">=", 0.97, "ratio", "")
	}
	if p, ok := byName["mid"]; ok {
		s.Check("perception.mid_noise_overall_accuracy", "overall field accuracy at 0.5% pixel noise", p.OverallAccuracy, ">=", 0.90, "ratio", "")
	}
	if p, ok := byName["high"]; ok {
		s.Check("perception.high_noise_overall_accuracy", "overall field accuracy at 1% pixel noise", p.OverallAccuracy, ">=", 0.85, "ratio", "")
		s.Check("perception.high_noise_hp_accuracy", "HP accuracy at 1% pixel noise", p.HPAccuracy, ">=", 0.90, "ratio", "")
		s.Check("perception.high_noise_enemy_hp_accuracy", "enemy HP accuracy at 1% pixel noise", p.EnemyHPAccuracy, ">=", 0.95, "ratio", "")
		s.Check("perception.high_noise_room_accuracy", "room accuracy at 1% pixel noise", p.RoomAccuracy, ">=", 0.95, "ratio", "")
		s.Check("perception.high_noise_potions_accuracy", "potion-count accuracy at 1% pixel noise", p.PotionsAccuracy, ">=", 0.80, "ratio", "potion icons are 7x7 and easiest to fragment under noise")
	}
	s.Details = map[string]any{
		"seeds":  cfg.perceptionSeeds,
		"steps":  cfg.perceptionSteps,
		"levels": results,
	}
	s.Notes = append(s.Notes,
		fmt.Sprintf("%d seeds x %d steps x %d noise levels; HP tolerance 1.0, enemy HP tolerance 1.0, room/potions exact.", len(cfg.perceptionSeeds), cfg.perceptionSteps, len(defaultNoiseLevels)),
		"GroundTruth is mockgame.GroundTruth(); the analyzer never reads it.",
	)
	s.Finalize()
	return s
}
