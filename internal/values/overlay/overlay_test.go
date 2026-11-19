package overlay

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

func TestHelmValuesMergeLayersAndParseSetsIntoTheResultLikeHelm(t *testing.T) {
	layers := []engine.Layer{
		{Name: "a.yaml", Data: []byte("m:\n  keep: 1\n  list: [a, b]\nservers:\n- host: x\n  port: 1\n")},
		{Name: "b.yaml", Data: []byte("m:\n  list: [c]\n")},
	}
	sets := []engine.SetSource{
		{Expression: "servers[0].host=y"},
		{Expression: "n=3,s=4", StringValues: false},
		{Expression: "t=5", StringValues: true},
	}
	got, err := HelmValues(layers, sets)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"m": map[string]any{"keep": float64(1), "list": []any{"c"}},
		// --set changes one list item and keeps its other keys, where a values file replaces the whole list.
		"servers": []any{map[string]any{"host": "y", "port": float64(1)}},
		"n":       int64(3), "s": int64(4), "t": "5",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %#v\nwant %#v", got, want)
	}
}

func TestHelmValuesNameTheBrokenSource(t *testing.T) {
	if _, err := HelmValues([]engine.Layer{{Name: "bad.yaml", Data: []byte("a: [")}}, nil); err == nil || !strings.Contains(err.Error(), "parse bad.yaml") {
		t.Fatalf("error = %v", err)
	}
	set := engine.SetSource{Expression: "nope", Source: engine.SourceLocation{File: "--set", Line: 2}}
	if _, err := HelmValues(nil, []engine.SetSource{set}); err == nil || !strings.Contains(err.Error(), "--set:2") {
		t.Fatalf("error = %v", err)
	}
}

func TestMergeAppliesTheApplicationsSetsBeforeTheUsers(t *testing.T) {
	o := &Overlay{TargetType: "t", Apps: []App{{Name: "a", Chart: "c", Sets: []engine.SetSource{{Expression: "v=app", Source: engine.SourceLocation{File: "p"}}}}}}
	app, err := o.App("a")
	if err != nil {
		t.Fatal(err)
	}
	r, err := o.Merge(app, engine.SetSources([]string{"v=user"}))
	if err != nil || r.Records["v"].EffectiveValue != "user" || len(r.Records["v"].History) != 2 || r.Target != "t" || r.Chart != "c" {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if len(o.Apps[0].Sets) != 1 {
		t.Fatal("Merge must not append the user's sets to the application")
	}
	if _, err := o.App("nope"); err == nil || !strings.Contains(err.Error(), `application "nope" does not exist in target type "t"`) {
		t.Fatalf("error = %v", err)
	}
}
