package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// summarizeBatch is the deterministic middle-tier compressor. It keeps the
// facts a later decision needs: what happened, how often, the numeric ranges
// and the tags involved. It never invents content.
func summarizeBatch(batch []Entry, gameID string, now time.Time) Entry {
	var b strings.Builder
	kinds := map[string]int{}
	tags := map[string]int{}
	values := map[string][2]float64{}
	haveValue := map[string]bool{}
	important := ""
	maxImp := 0.0
	for _, e := range batch {
		kinds[string(e.Kind)]++
		if e.Importance > maxImp {
			maxImp = e.Importance
			important = e.Text
		}
		for _, t := range e.Tags {
			tags[t]++
		}
		for k, v := range e.Values {
			r := values[k]
			if !haveValue[k] {
				r = [2]float64{v, v}
				haveValue[k] = true
			} else {
				if v < r[0] {
					r[0] = v
				}
				if v > r[1] {
					r[1] = v
				}
			}
			values[k] = r
		}
	}
	b.WriteString(fmt.Sprintf("%d events [", len(batch)))
	ks := make([]string, 0, len(kinds))
	for k := range kinds {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for i, k := range ks {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(fmt.Sprintf("%s:%d", k, kinds[k]))
	}
	b.WriteString("]")
	if len(tags) > 0 {
		ts := make([]string, 0, len(tags))
		for t := range tags {
			ts = append(ts, t)
		}
		sort.Strings(ts)
		if len(ts) > 10 {
			ts = ts[:10]
		}
		b.WriteString(" tags=" + strings.Join(ts, ","))
	}
	if len(values) > 0 {
		vk := make([]string, 0, len(values))
		for k := range values {
			vk = append(vk, k)
		}
		sort.Strings(vk)
		b.WriteString(" values=")
		for i, k := range vk {
			if i > 0 {
				b.WriteString("; ")
			}
			r := values[k]
			if r[0] == r[1] {
				b.WriteString(fmt.Sprintf("%s=%g", k, r[0]))
			} else {
				b.WriteString(fmt.Sprintf("%s=%g..%g", k, r[0], r[1]))
			}
		}
	}
	if important != "" {
		b.WriteString(" key=" + trimRunes(important, 220))
	}
	return Entry{
		Tier:       TierSummary,
		Kind:       KindSummary,
		Text:       b.String(),
		Tags:       topTags(tags, 8),
		GameID:     gameID,
		CreatedAt:  now,
		Importance: maxImp,
	}
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func topTags(tags map[string]int, n int) []string {
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(tags))
	for k, v := range tags {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v == list[j].v {
			return list[i].k < list[j].k
		}
		return list[i].v > list[j].v
	})
	if len(list) > n {
		list = list[:n]
	}
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.k
	}
	return out
}
