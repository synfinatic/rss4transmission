package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// PreferDimension declares a preference ordering for a single label.
type PreferDimension struct {
	Label string   `koanf:"label"`
	Order []string `koanf:"order"`
}

// Group is a set of Require constraints within a feed config.
type Group struct {
	Require map[string][]string `koanf:"Require"`
}

// Matches returns true if all Require constraints are satisfied by labels.
func (g *Group) Matches(labels map[string]string) bool {
	for label, acceptable := range g.Require {
		v, ok := labels[label]
		if !ok || !slices.Contains(acceptable, v) {
			return false
		}
	}
	return true
}

// IdentityKey computes a stable string key from the given labels using the
// declared identity label names. Returns ("", false) if any identity label is
// absent from labels.
func IdentityKey(labels map[string]string, identityLabels []string) (string, bool) {
	parts := make([]string, len(identityLabels))
	for i, name := range identityLabels {
		v, ok := labels[name]
		if !ok {
			return "", false
		}
		parts[i] = fmt.Sprintf("%s=%s", name, v)
	}
	return strings.Join(parts, "|"), true
}

// MergeLabels returns a new map with titleLabels as the base, overridden by
// fileLabels. Neither input map is modified.
func MergeLabels(titleLabels, fileLabels map[string]string) map[string]string {
	merged := make(map[string]string, len(titleLabels)+len(fileLabels))
	maps.Copy(merged, titleLabels)
	maps.Copy(merged, fileLabels)
	return merged
}

// PreferenceRank returns a rank slice for labels against the preference
// dimensions. Lower values are better. A missing or unrecognised label value
// ranks as len(dim.Order) (worst possible).
func PreferenceRank(labels map[string]string, prefer []PreferDimension) []int {
	rank := make([]int, len(prefer))
	for i, dim := range prefer {
		rank[i] = len(dim.Order) // default: worst rank
		v, ok := labels[dim.Label]
		if !ok {
			continue
		}
		for j, o := range dim.Order {
			if o == v {
				rank[i] = j
				break
			}
		}
	}
	return rank
}

// withDefaultLabels returns labels with any absent defaults filled in.
// If no defaults are needed, it returns labels unchanged (no allocation).
func withDefaultLabels(labels, defaults map[string]string) map[string]string {
	if len(defaults) == 0 {
		return labels
	}
	var result map[string]string
	for k, v := range defaults {
		if _, ok := labels[k]; !ok {
			if result == nil {
				result = make(map[string]string, len(labels)+len(defaults))
				maps.Copy(result, labels)
			}
			result[k] = v
		}
	}
	if result != nil {
		return result
	}
	return labels
}

// IsBetter returns true if rank a is strictly better (lexicographically lower)
// than rank b.
func IsBetter(a, b []int) bool {
	for i := range a {
		if i >= len(b) {
			return false
		}
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return false
}
