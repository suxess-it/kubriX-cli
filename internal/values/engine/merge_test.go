package engine

import (
	"reflect"
	"strings"
	"testing"
)

// mergeLayers merges bodies as layers a.yaml, b.yaml, ... followed by user --set expressions.
func mergeLayers(t *testing.T, sets []string, bodies ...string) *Result {
	t.Helper()
	r, err := Merge(layers(bodies...), SetSources(sets), Options{Target: "t", Application: "app", Chart: "chart"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func layers(bodies ...string) []Layer {
	out := make([]Layer, len(bodies))
	for i, b := range bodies {
		out[i] = Layer{Name: string(rune('a'+i)) + ".yaml", Data: []byte(b)}
	}
	return out
}

func effective(t *testing.T, r *Result, path string) any {
	t.Helper()
	rec, ok := r.Records[path]
	if !ok {
		t.Fatalf("no record for %s; records: %v", path, SortedPaths(r.Records))
	}
	return rec.EffectiveValue
}

func TestMergeFollowsHelmValuesSemantics(t *testing.T) {
	cases := []struct {
		name   string
		bodies []string
		path   string
		want   any
	}{
		{"maps merge recursively", []string{"a:\n  keep: yes\n  b: 1\n", "a:\n  b: 2\n"}, "a.keep", "yes"},
		{"a later scalar replaces an earlier one", []string{"a: 1\n", "a: 2\n"}, "a", 2},
		{"a later list replaces the whole list", []string{"l: [a, b]\n", "l: [c]\n"}, "l", []any{"c"}},
		{"an explicit null replaces instead of deleting", []string{"v: present\n", "v: null\n"}, "v", nil},
		{"literal dots in keys are escaped in the path", []string{"labels:\n  app.kubernetes.io/name: demo\n"}, `labels.app\.kubernetes\.io/name`, "demo"},
		{"an empty layer is an empty map", []string{"", "a: 1\n"}, "a", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effective(t, mergeLayers(t, nil, c.bodies...), c.path); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s = %#v, want %#v", c.path, got, c.want)
			}
		})
	}
}

func TestEachAssignmentIsClassifiedAgainstThePreviousValue(t *testing.T) {
	cases := []struct {
		name   string
		bodies []string
		path   string
		want   AssignmentAction
	}{
		{"a different value of the same kind overrides", []string{"v: 1\n", "v: 2\n"}, "v", ActionOverridden},
		{"the same value again is an identical redefinition", []string{"v: 3\n", "v: 3\n"}, "v", ActionIdenticalRedefinition},
		{"a scalar replaced by a map changes type", []string{"v: x\n", "v:\n  c: 1\n"}, "v", ActionTypeChanged},
		{"null replacing a value changes type", []string{"v: x\n", "v: null\n"}, "v", ActionTypeChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := mergeLayers(t, nil, c.bodies...).Records[c.path].History
			if h[0].Action != ActionDefined || h[len(h)-1].Action != c.want {
				t.Fatalf("history = %+v, want defined then %s", h, c.want)
			}
		})
	}
}

func TestChildrenOfAReplacedMapAreNoLongerRecorded(t *testing.T) {
	r := mergeLayers(t, nil, "m:\n  child: true\n", "m: x\n")
	if _, ok := r.Records["m.child"]; ok {
		t.Fatal("m.child is still recorded after m became a scalar")
	}
}

func TestHistoryKeepsOrderSourcePositionsAndMarksOnlyTheLastEffective(t *testing.T) {
	r := mergeLayers(t, nil, "v: 1\n", "x: 0\nv: 2\n", "v: 3\n")
	h := r.Records["v"].History
	want := []SourceLocation{{"a.yaml", 1, 4}, {"b.yaml", 2, 4}, {"c.yaml", 1, 4}}
	for i, a := range h {
		if a.Source != want[i] || a.Value != i+1 || a.Effective != (i == 2) {
			t.Fatalf("assignment %d = %+v, want source %+v, value %d, effective only last", i, a, want[i], i+1)
		}
	}
	if r.Records["v"].EffectiveSource != want[2] {
		t.Fatalf("effective source = %+v", r.Records["v"].EffectiveSource)
	}
}

func TestALayerThatChangesNothingIsMarkedUnchanged(t *testing.T) {
	r := mergeLayers(t, nil, "v: 3\n", "v: 3\n", "w: 1\n")
	got := []bool{r.Files[0].Changed, r.Files[1].Changed, r.Files[2].Changed}
	if !reflect.DeepEqual(got, []bool{true, false, true}) {
		t.Fatalf("changed = %v, want [true false true]", got)
	}
}

func TestSetSourcesComeAfterEveryLayerWithTheirOwnProvenance(t *testing.T) {
	r := mergeLayers(t, []string{"app.replicas=3,app.enabled=true", "app.items={one,two}"}, "app:\n  replicas: 1\n  enabled: false\n  items: [old]\n")
	if effective(t, r, "app.replicas") != 3 || effective(t, r, "app.enabled") != true {
		t.Fatalf("replicas=%#v enabled=%#v", effective(t, r, "app.replicas"), effective(t, r, "app.enabled"))
	}
	if !reflect.DeepEqual(effective(t, r, "app.items"), []any{"one", "two"}) {
		t.Fatalf("items = %#v", effective(t, r, "app.items"))
	}
	if src := r.Records["app.replicas"].EffectiveSource; src != (SourceLocation{File: "--set", Line: 1, Column: 1}) {
		t.Fatalf("replicas source = %+v", src)
	}
	if h := r.Records["app.items"].History; h[1].Source.Line != 2 || h[1].Kind != KindSequence {
		t.Fatalf("items history = %+v", h)
	}
}

func TestSetSupportsHelmIndexesEscapesNullAndOrdering(t *testing.T) {
	r := mergeLayers(t, []string{`servers[0].host=one,labels.app\.kubernetes\.io/name=demo,value=null`, "servers[0].host=two"})
	if effective(t, r, `labels.app\.kubernetes\.io/name`) != "demo" || effective(t, r, "value") != nil {
		t.Fatalf("records = %v", SortedPaths(r.Records))
	}
	if h := r.Records["servers"].History; len(h) != 2 || h[1].Source.Line != 2 {
		t.Fatalf("servers history = %+v", h)
	}
}

func TestStringSetSourcesKeepValuesAsStrings(t *testing.T) {
	r, err := Merge(nil, []SetSource{{Expression: "n=3,b=true", StringValues: true, Source: SourceLocation{File: "p"}}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if effective(t, r, "n") != "3" || effective(t, r, "b") != "true" {
		t.Fatalf("n=%#v b=%#v", effective(t, r, "n"), effective(t, r, "b"))
	}
}

func TestInvalidInputIsReportedWithItsSource(t *testing.T) {
	cases := []struct {
		name   string
		layers []Layer
		sets   []string
		want   string
	}{
		{"a set expression without =", nil, []string{"missing-equals"}, "parse set source --set:1"},
		{"a document root that is not a mapping", layers("- a\n"), nil, `"a.yaml": document root must be a mapping`},
		{"malformed YAML", layers("a: [\n"), nil, `parse values file "a.yaml"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Merge(c.layers, SetSources(c.sets), Options{}); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestOverridesListsOnlyLeavesAssignedMoreThanOnce(t *testing.T) {
	r := mergeLayers(t, nil, "m:\n  a: 1\n  b: 1\nonce: 1\n", "m:\n  a: 2\n")
	if got := Overrides(r); !reflect.DeepEqual(got, []string{"m.a"}) {
		t.Fatalf("overrides = %v, want [m.a] (not the map m, not once or m.b)", got)
	}
}
