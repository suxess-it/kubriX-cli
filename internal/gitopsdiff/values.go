package gitopsdiff

import (
	"reflect"
	"slices"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

// valueChanges compares the effective leaf values of an application on both revisions. A nil result means the
// application is not installed in that revision, so all its values are added or removed.
func valueChanges(chart, testCase string, base, head *engine.Result) []ValueChange {
	before, after := leaves(base), leaves(head)
	paths := make([]string, 0, len(before)+len(after))
	for p := range before {
		paths = append(paths, p)
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	var out []ValueChange
	for _, p := range paths {
		b, inBase := before[p]
		a, inHead := after[p]
		c := ValueChange{Chart: chart, TestCase: testCase, Path: p}
		switch {
		case !inBase:
			c.Kind, c.After, c.Source = Added, a.EffectiveValue, a.EffectiveSource
		case !inHead:
			c.Kind, c.Before, c.Source = Removed, b.EffectiveValue, b.EffectiveSource
		case !reflect.DeepEqual(a.EffectiveValue, b.EffectiveValue):
			c.Kind, c.Before, c.After, c.Source = Modified, b.EffectiveValue, a.EffectiveValue, a.EffectiveSource
		default:
			continue
		}
		out = append(out, c)
	}
	return out
}

// leaves are the records of values that are not maps: a changed map is reported by its changed leaves.
func leaves(r *engine.Result) map[string]*engine.ValueRecord {
	out := map[string]*engine.ValueRecord{}
	if r == nil {
		return out
	}
	for p, rec := range r.Records {
		if _, isMap := rec.EffectiveValue.(map[string]any); !isMap {
			out[p] = rec
		}
	}
	return out
}
