package memory

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Scored struct {
	Entry Entry   `json:"entry"`
	Score float64 `json:"score"`
}

// tokenize handles Latin words and CJK runes (each CJK rune is a token, with
// bigrams added for better Chinese matching).
func tokenize(s string) []string {
	s = strings.ToLower(s)
	var out []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			out = append(out, string(word))
			word = word[:0]
		}
	}
	prevCJK := rune(0)
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			flush()
			out = append(out, string(r))
			if prevCJK != 0 {
				out = append(out, string(prevCJK)+string(r))
			}
			prevCJK = r
			continue
		}
		prevCJK = 0
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			word = append(word, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func querySet(q string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, t := range tokenize(q) {
		m[t] = struct{}{}
	}
	return m
}

func recall(query string, entries []Entry, k int, now time.Time) []Scored {
	if k <= 0 {
		k = 5
	}
	qt := querySet(query)
	if len(entries) == 0 {
		return nil
	}
	// document frequency for a light IDF
	df := map[string]int{}
	docs := make([][]string, len(entries))
	for i, e := range entries {
		toks := tokenize(e.Text + " " + strings.Join(e.Tags, " "))
		docs[i] = toks
		seen := map[string]bool{}
		for _, t := range toks {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	n := float64(len(entries))
	scores := make([]Scored, 0, len(entries))
	for i, e := range entries {
		if len(qt) == 0 {
			scores = append(scores, Scored{Entry: e, Score: e.retentionScore(now)})
			continue
		}
		var overlap float64
		seen := map[string]bool{}
		for _, t := range docs[i] {
			if seen[t] {
				continue
			}
			seen[t] = true
			if _, ok := qt[t]; ok {
				idf := math.Log(1 + n/(1+float64(df[t])))
				overlap += 1 + idf
			}
		}
		if overlap == 0 {
			// still allow recency-based recall for very important facts
			if e.Importance < 0.8 {
				continue
			}
		}
		norm := overlap / (1 + math.Log(1+float64(len(docs[i]))))
		score := norm*1.0 + e.Importance*0.6 + e.retentionScore(now)*0.2
		scores = append(scores, Scored{Entry: e, Score: score})
	}
	sort.SliceStable(scores, func(i, j int) bool { return scores[i].Score > scores[j].Score })
	if len(scores) > k {
		scores = scores[:k]
	}
	return scores
}

// Recall searches all tiers and bumps access counters.
func (m *InstanceMemory) Recall(query string, k int) []Scored {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k <= 0 {
		k = 5
	}
	now := time.Now()
	cands := make([]Entry, 0, len(m.Facts)+len(m.Summaries)+len(m.Working))
	cands = append(cands, m.Working...)
	cands = append(cands, m.Summaries...)
	cands = append(cands, m.Facts...)
	if len(cands) > recallCandidatePool {
		cands = cands[len(cands)-recallCandidatePool:]
	}
	res := recall(query, cands, k, now)
	m.Stats.Recalls++
	for _, r := range res {
		bumpAccess(m.Working, r.Entry.ID)
		bumpAccess(m.Summaries, r.Entry.ID)
		bumpAccess(m.Facts, r.Entry.ID)
	}
	return res
}

func bumpAccess(entries []Entry, id string) {
	for i := range entries {
		if entries[i].ID == id {
			entries[i].Accesses++
			entries[i].LastUsedAt = time.Now()
			return
		}
	}
}
