package eval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"goplay/internal/memory"
)

type recallResult struct {
	K      int      `json:"k"`
	Hits   int      `json:"hits"`
	Total  int      `json:"total"`
	Rate   float64  `json:"rate"`
	Missed []string `json:"missed,omitempty"`
}

func recallAt(m *memory.InstanceMemory, tokens []string, k int) recallResult {
	r := recallResult{K: k, Total: len(tokens)}
	for _, tok := range tokens {
		hits := m.Recall(tok, k)
		found := false
		for _, h := range hits {
			if strings.Contains(strings.ToLower(h.Entry.Text), tok) {
				found = true
				break
			}
		}
		if found {
			r.Hits++
		} else {
			r.Missed = append(r.Missed, tok)
		}
	}
	if r.Total > 0 {
		r.Rate = float64(r.Hits) / float64(r.Total)
	}
	return r
}

func statsMap(st memory.Stats) map[string]any {
	return map[string]any{
		"added":       st.Added,
		"compactions": st.Compactions,
		"evictions":   st.Evictions,
		"recalls":     st.Recalls,
		"bytes":       st.Bytes,
		"working":     st.Working,
		"summaries":   st.Summaries,
		"facts":       st.Facts,
	}
}

func fillerText(i int) string {
	return fmt.Sprintf("routine filler note %04d: patrol log entry with no durable value; "+
		"the corridor was quiet and this observation is intentionally unimportant; "+
		"filler-token-%04d should be evicted under budget pressure and must not crowd out safety rules. "+
		"Additional padding keeps the entry realistic without adding meaning.", i, i)
}

// runMemory measures Recall@1/3/5 under a fixed 256KB budget.
//
// Scenario A ("eviction"): a large body of low-importance filler and event
// noise forces compaction and eviction; 40 manually-labeled must-remember
// facts are injected after the filler, then more noise is added. The must
// facts must still be recallable after compaction.
//
// Scenario B ("summarization"): 128 events with one must event every 8th are
// folded into summaries; each summary retains the batch's most important key,
// so the must-remember events must remain recallable.
func runMemory(ctx context.Context, cfg evalConfig) (s Suite) {
	s = Suite{ID: "memory", Title: "Memory recall under a 256KB budget"}
	start := time.Now()
	defer func() { s.DurationMS = time.Since(start).Milliseconds() }()

	const budgetKB = 256
	budgetBytes := budgetKB * 1024

	// ---- Scenario A: eviction pressure ----
	a := memory.NewInstance("eval-memory-eviction", budgetKB)
	for i := 0; i < cfg.memoryFillers; i++ {
		if err := ctx.Err(); err != nil {
			s.Error = err.Error()
			s.Finalize()
			return s
		}
		a.Add(memory.Entry{
			Kind: memory.KindFact, Tier: memory.TierFact,
			Text: fillerText(i), Importance: 0.2, Tags: []string{"filler", "noise"},
		})
	}
	const mustCount = 40
	var mustTokens []string
	for i := 0; i < mustCount; i++ {
		tok := fmt.Sprintf("must-code-%02d", i)
		mustTokens = append(mustTokens, tok)
		a.Add(memory.Entry{
			Kind: memory.KindFact, Tier: memory.TierFact,
			Text: fmt.Sprintf("MUST REMEMBER %s: operator safety rule %02d is non-negotiable and must survive compaction; "+
				"never attack while the human is in control.", tok, i),
			Importance: 0.95, Tags: []string{"must", "safety", "operator"},
		})
	}
	for i := 0; i < cfg.memoryNoiseEvents; i++ {
		a.Add(memory.Entry{
			Kind: memory.KindEvent, Text: fmt.Sprintf("ambient event %04d: an unimportant patrol observation arrived", i),
			Importance: 0.15, Tags: []string{"noise", "event"},
		})
	}
	stA := a.StatsSnapshot()
	// Recall after compaction: measure on the post-noise state.
	a1 := recallAt(a, mustTokens, 1)
	a3 := recallAt(a, mustTokens, 3)
	a5 := recallAt(a, mustTokens, 5)

	// ---- Scenario B: summarization ----
	b := memory.NewInstance("eval-memory-summary", budgetKB)
	const bEvents = 128
	var bTokens []string
	for i := 0; i < bEvents; i++ {
		if i%8 == 0 {
			tok := fmt.Sprintf("must-summary-%02d", i/8)
			bTokens = append(bTokens, tok)
			b.Add(memory.Entry{
				Kind: memory.KindEvent, Text: fmt.Sprintf("MUST REMEMBER %s: checkpoint %02d needs review after compression", tok, i/8),
				Importance: 0.90, Tags: []string{"must", "checkpoint"},
			})
		} else {
			b.Add(memory.Entry{
				Kind: memory.KindEvent, Text: fmt.Sprintf("ambient-summary-event-%03d routine chatter without durable value", i),
				Importance: 0.10, Tags: []string{"noise"},
			})
		}
	}
	stB := b.StatsSnapshot()
	b1 := recallAt(b, bTokens, 1)
	b3 := recallAt(b, bTokens, 3)
	b5 := recallAt(b, bTokens, 5)

	s.Add("memory.eviction.budget_bytes", "fixed memory budget", float64(budgetBytes), "bytes", "")
	s.Add("memory.eviction.bytes_after", "bytes used after noise (scenario A)", float64(stA.Bytes), "bytes", "")
	s.Add("memory.eviction.compactions", "compactions (scenario A)", float64(stA.Compactions), "count", "")
	s.Add("memory.eviction.evictions", "evictions (scenario A)", float64(stA.Evictions), "count", "")
	s.Add("memory.eviction.summaries", "summaries (scenario A)", float64(stA.Summaries), "count", "")
	s.Add("memory.eviction.must_recall_at_1", "Recall@1 on must-remember facts", a1.Rate, "ratio", "")
	s.Add("memory.eviction.must_recall_at_3", "Recall@3 on must-remember facts", a3.Rate, "ratio", "")
	s.Add("memory.eviction.must_recall_at_5", "Recall@5 on must-remember facts", a5.Rate, "ratio", "")
	s.Check("memory.eviction.budget_respected", "memory stays within the 256KB budget", float64(stA.Bytes), "<=", float64(budgetBytes), "bytes", "")
	s.Check("memory.eviction.compaction_ran", "at least one compaction happened", float64(stA.Compactions), ">=", 1, "count", "")
	s.Check("memory.eviction.eviction_ran", "at least one low-value entry was evicted", float64(stA.Evictions), ">=", 1, "count", "")
	s.Check("memory.eviction.recall_at_1", "must-remember Recall@1 after compaction", a1.Rate, ">=", 0.90, "ratio", "40 labeled facts")
	s.Check("memory.eviction.recall_at_3", "must-remember Recall@3 after compaction", a3.Rate, ">=", 0.95, "ratio", "40 labeled facts")
	s.Check("memory.eviction.recall_at_5", "must-remember Recall@5 after compaction", a5.Rate, ">=", 0.95, "ratio", "40 labeled facts")

	s.Add("memory.summary.summaries", "summaries produced (scenario B)", float64(stB.Summaries), "count", "")
	s.Add("memory.summary.working", "working entries remaining (scenario B)", float64(stB.Working), "count", "")
	s.Add("memory.summary.must_recall_at_1", "Recall@1 on must-remember events", b1.Rate, "ratio", "")
	s.Add("memory.summary.must_recall_at_3", "Recall@3 on must-remember events", b3.Rate, "ratio", "")
	s.Add("memory.summary.must_recall_at_5", "Recall@5 on must-remember events", b5.Rate, "ratio", "")
	s.Check("memory.summary.compaction_ran", "working events were folded into summaries", float64(stB.Compactions), ">=", 1, "count", "")
	s.Check("memory.summary.summaries_created", "at least 8 summaries retained the must keys", float64(stB.Summaries), ">=", 8, "count", "")
	s.Check("memory.summary.recall_at_1", "must-remember event Recall@1 after compression", b1.Rate, ">=", 0.90, "ratio", "")
	s.Check("memory.summary.recall_at_3", "must-remember event Recall@3 after compression", b3.Rate, ">=", 0.95, "ratio", "")
	s.Check("memory.summary.recall_at_5", "must-remember event Recall@5 after compression", b5.Rate, ">=", 0.95, "ratio", "")

	s.Details = map[string]any{
		"budget_kb": budgetKB,
		"eviction": map[string]any{
			"fillers": cfg.memoryFillers, "must_entries": mustCount, "noise_events": cfg.memoryNoiseEvents,
			"stats": statsMap(stA), "recall_at_1": a1, "recall_at_3": a3, "recall_at_5": a5,
		},
		"summarization": map[string]any{
			"events": bEvents, "must_entries": len(bTokens), "stats": statsMap(stB),
			"recall_at_1": b1, "recall_at_3": b3, "recall_at_5": b5,
		},
	}
	s.Notes = append(s.Notes,
		"Recall is counted as hit only when the correct labeled entry appears in the top K and its text contains the unique token.",
		"Must-remember facts use importance 0.95, which the memory package protects from eviction.",
	)
	s.Finalize()
	return s
}
