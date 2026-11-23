package scheduler

import (
	"sort"
	"strings"
	"time"

	"goplay/internal/domain"
)

// Adapt is the instance-side adapter for global preferences. It does two
// things and only two things:
//  1. drop a preference whose avoidance conflicts with a known instance
//     requirement (instance constraints win);
//  2. turn preferences into soft weights/hints.
//
// It never produces a hard constraint.
func Adapt(prefs []domain.Preference, rules domain.InstanceRules, now time.Time) domain.Control {
	ctl := domain.Control{Weights: domain.SoftWeights{}}
	sort.Slice(prefs, func(i, j int) bool {
		if prefs[i].UpdatedAt.Equal(prefs[j].UpdatedAt) {
			return prefs[i].ID < prefs[j].ID
		}
		return prefs[i].UpdatedAt.Before(prefs[j].UpdatedAt)
	})
	for _, p := range prefs {
		conflict, why := conflicts(p, rules)
		if conflict {
			ctl.Adaptations = append(ctl.Adaptations, domain.Adaptation{
				PreferenceID: p.ID,
				Preference:   p.Text,
				Outcome:      "suppressed",
				Reason:       why,
				At:           now,
			})
			continue
		}
		for k, v := range p.Weights {
			ctl.Weights[k] += v
		}
		if p.Text != "" {
			ctl.Hints = append(ctl.Hints, p.Text)
		}
		ctl.Adaptations = append(ctl.Adaptations, domain.Adaptation{
			PreferenceID: p.ID,
			Preference:   p.Text,
			Outcome:      "applied",
			Reason:       "converted to soft weights",
			At:           now,
		})
	}
	for k, v := range ctl.Weights {
		if v > 1 {
			ctl.Weights[k] = 1
		}
		if v < -1 {
			ctl.Weights[k] = -1
		}
	}
	return ctl
}

func conflicts(p domain.Preference, rules domain.InstanceRules) (bool, string) {
	if len(p.AppliesTo) > 0 {
		// AppliesTo is optional; an empty list means all games. When set, the
		// runner prefers explicit matches but a non-match is not a conflict.
	}
	required := map[string]bool{}
	for _, t := range rules.RequiredTags {
		required[strings.ToLower(t)] = true
	}
	for _, t := range p.AvoidTags {
		if required[strings.ToLower(t)] {
			return true, "global preference avoids tag " + t + " but the instance requires it; instance rule wins"
		}
	}
	return false, ""
}
