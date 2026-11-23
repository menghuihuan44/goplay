package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Registry is a deliberately small metrics registry. It supports counters,
// gauges and histograms with exact percentiles over a bounded sample window.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]float64
	gauges   map[string]float64
	hists    map[string]*hist
	since    time.Time
}

type hist struct {
	samples []float64
	max     int
	sum     float64
	count   int64
}

func New() *Registry {
	return &Registry{
		counters: map[string]float64{},
		gauges:   map[string]float64{},
		hists:    map[string]*hist{},
		since:    time.Now(),
	}
}

func (r *Registry) Inc(name string, delta float64) {
	r.mu.Lock()
	r.counters[name] += delta
	r.mu.Unlock()
}

func (r *Registry) Set(name string, v float64) {
	r.mu.Lock()
	r.gauges[name] = v
	r.mu.Unlock()
}

func (r *Registry) Observe(name string, v float64) {
	r.mu.Lock()
	h := r.hists[name]
	if h == nil {
		h = &hist{max: 2048}
		r.hists[name] = h
	}
	h.count++
	h.sum += v
	if len(h.samples) < h.max {
		h.samples = append(h.samples, v)
	} else {
		h.samples[h.count%int64(h.max)] = v
	}
	r.mu.Unlock()
}

func (r *Registry) Counter(name string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.counters[name]
}

func (r *Registry) Gauge(name string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.gauges[name]
}

func (r *Registry) Hist(name string) (count int64, avg, p50, p95, p99 float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h := r.hists[name]
	if h == nil || h.count == 0 {
		return 0, 0, 0, 0, 0
	}
	vals := append([]float64(nil), h.samples...)
	sort.Float64s(vals)
	return h.count, h.sum / float64(h.count), pct(vals, 0.50), pct(vals, 0.95), pct(vals, 0.99)
}

func pct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

type Snapshot struct {
	UptimeSeconds float64            `json:"uptime_seconds"`
	Counters      map[string]float64 `json:"counters"`
	Gauges        map[string]float64 `json:"gauges"`
	Histograms    map[string]Histo   `json:"histograms"`
}

type Histo struct {
	Count int64   `json:"count"`
	Avg   float64 `json:"avg"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
}

func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := Snapshot{
		UptimeSeconds: time.Since(r.since).Seconds(),
		Counters:      map[string]float64{},
		Gauges:        map[string]float64{},
		Histograms:    map[string]Histo{},
	}
	for k, v := range r.counters {
		s.Counters[k] = v
	}
	for k, v := range r.gauges {
		s.Gauges[k] = v
	}
	for k, h := range r.hists {
		if h.count == 0 {
			continue
		}
		vals := append([]float64(nil), h.samples...)
		sort.Float64s(vals)
		s.Histograms[k] = Histo{
			Count: h.count,
			Avg:   h.sum / float64(h.count),
			P50:   pct(vals, 0.5),
			P95:   pct(vals, 0.95),
			P99:   pct(vals, 0.99),
		}
	}
	return s
}

// Prometheus renders the registry in text exposition format.
func (r *Registry) Prometheus() string {
	s := r.Snapshot()
	var b strings.Builder
	keys := make([]string, 0, len(s.Counters))
	for k := range s.Counters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := sanitize(k)
		fmt.Fprintf(&b, "goplay_%s_total %g\n", name, s.Counters[k])
	}
	keys = keys[:0]
	for k := range s.Gauges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := sanitize(k)
		fmt.Fprintf(&b, "goplay_%s %g\n", name, s.Gauges[k])
	}
	keys = keys[:0]
	for k := range s.Histograms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h := s.Histograms[k]
		name := sanitize(k)
		fmt.Fprintf(&b, "goplay_%s_count %d\n", name, h.Count)
		fmt.Fprintf(&b, "goplay_%s_avg %g\n", name, h.Avg)
		fmt.Fprintf(&b, "goplay_%s_p50 %g\n", name, h.P50)
		fmt.Fprintf(&b, "goplay_%s_p95 %g\n", name, h.P95)
		fmt.Fprintf(&b, "goplay_%s_p99 %g\n", name, h.P99)
	}
	return b.String()
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Well-known metric names.
const (
	DecisionLatencyMS    = "decision_latency_ms"
	ActionLatencyMS      = "action_latency_ms"
	PerceptionLatencyMS  = "perception_latency_ms"
	MemoryRecallMS       = "memory_recall_ms"
	HandoverLatencyMS    = "handover_latency_ms"
	InterventionRate     = "user_interventions_per_boundary"
	ConstraintApplyCount = "constraints_applied_total"
	PreferenceApplyCount = "preferences_applied_total"
	ActionsTotal         = "actions_total"
	PolicyFallbackTotal  = "policy_fallback_total"
	SafetyViolationTotal = "safety_violations_total"
	HumanTakeoverTotal   = "human_takeovers_total"
	InstanceErrorsTotal  = "instance_errors_total"
	MemoryEntriesGauge   = "memory_entries"
	MemoryBytesGauge     = "memory_bytes"
	PerceptionAccuracy   = "perception_state_accuracy"
	RecallAtK            = "memory_recall_at_k"
	LearningConvergence  = "learning_attempts_to_threshold"
	LocalTTFTMS          = "local_ttft_ms"
	LocalTPS             = "local_tokens_per_second"
	SafetyAuditOK        = "safety_static_audit_ok"
	ConcurrentInstances  = "concurrent_instances"
)
