package lint

import (
	"reflect"
	"slices"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

func analyze(t *testing.T, sets []string, bodies ...string) []Finding {
	t.Helper()
	var layers []engine.Layer
	for i, b := range bodies {
		layers = append(layers, engine.Layer{Name: string(rune('a'+i)) + ".yaml", Data: []byte(b)})
	}
	r, err := engine.Merge(layers, engine.SetSources(sets), engine.Options{Application: "app", Chart: "app"})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(layers))
	for i, l := range layers {
		names[i] = l.Name
	}
	return Analyze(r, names)
}

func rules(findings []Finding) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.Rule)
	}
	return out
}

// Each rule fires on its trigger and stays silent on the closest input that is not one.
func TestEachRuleFiresOnlyOnItsTrigger(t *testing.T) {
	cases := []struct {
		name   string
		bodies []string
		sets   []string
		want   []string
	}{
		{"an identical redefinition", []string{"v: 1\nw: 1\n", "v: 1\nw: 2\n"}, nil, []string{"identical-redefinition"}},
		{"a type change", []string{"v: x\n", "v:\n  n: 1\n"}, nil, []string{"type-change"}},
		{"a list replacing a different list", []string{"l: [a]\n", "l: [b]\n"}, nil, []string{"list-replacement"}},
		{"an identical list is only an identical redefinition", []string{"l: [a]\nw: 1\n", "l: [a]\nw: 2\n"}, nil, []string{"identical-redefinition"}},
		{"a list replacing a scalar is a type change, not a list replacement", []string{"l: a\n", "l: [a]\n"}, nil, []string{"type-change"}},
		{"a file that changes nothing", []string{"v: 1\n", "v: 1\n"}, nil, []string{"identical-redefinition", "no-op-file"}},
		{"a repeated map is reported by its repeated leaves only", []string{"m:\n  a: 1\n", "m:\n  a: 1\n"}, nil, []string{"identical-redefinition", "no-op-file"}},
		{"an override of a different value", []string{"v: 1\n", "v: 2\n"}, nil, nil},
		{"set sources take part in the history", []string{"items: [one]\nmode: simple\nreplicas: 2\n"}, []string{"items={two},mode.nested=true,replicas=2"}, []string{"identical-redefinition", "list-replacement", "type-change"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rules(analyze(t, c.sets, c.bodies...))
			slices.Sort(got)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("rules = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAFileListedTwiceIsADuplicate(t *testing.T) {
	r, err := engine.Merge([]engine.Layer{{Name: "a.yaml", Data: []byte("v: 1\n")}}, nil, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := Analyze(r, []string{"dir/a.yaml", "dir/./a.yaml"})
	if len(got) != 1 || got[0].Rule != "duplicate-file" || got[0].Source.File != "dir/./a.yaml" || got[0].Previous.File != "dir/a.yaml" {
		t.Fatalf("findings = %+v", got)
	}
}

func TestFindingsAreSortedByFileLinePathAndRule(t *testing.T) {
	got := analyze(t, nil, "b: x\na: [1]\n", "a: [2]\nb:\n  n: 1\n")
	var order []string
	for _, f := range got {
		order = append(order, f.Source.File+":"+f.Path+":"+f.Rule)
	}
	want := []string{"b.yaml:a:list-replacement", "b.yaml:b:type-change"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestReachesComparesTheHighestSeverityWithTheThreshold(t *testing.T) {
	info := []Finding{{Severity: "info"}}
	warning := []Finding{{Severity: "info"}, {Severity: "warning"}}
	cases := []struct {
		findings  []Finding
		threshold string
		want      bool
	}{
		{nil, "info", false},
		{info, "info", true},
		{info, "warning", false},
		{warning, "warning", true},
		{warning, "error", false},
		{warning, "never", false},
	}
	for _, c := range cases {
		if got := Reaches(c.findings, c.threshold); got != c.want {
			t.Errorf("Reaches(%v, %s) = %v, want %v", c.findings, c.threshold, got, c.want)
		}
	}
}
