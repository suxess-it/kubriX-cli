package output

import (
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/values/engine"
)

func TestYAMLSortsKeysAndAnnotatesEachLeafWithItsSource(t *testing.T) {
	r, err := engine.Merge([]engine.Layer{
		{Name: "a.yaml", Data: []byte("z: 2\nm:\n  leaf: 1\n  list: [x]\n")},
		{Name: "b.yaml", Data: []byte("m:\n  leaf: 3\n")},
	}, nil, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := YAML(r, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "m:\n  leaf: 3\n  list:\n    - x\nz: 2\n"; string(plain) != want {
		t.Fatalf("yaml =\n%s\nwant\n%s", plain, want)
	}
	annotated, err := YAML(r, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "m:\n  # source: b.yaml:2\n  leaf: 3\n  # source: a.yaml:4\n  list:\n    - x\n# source: a.yaml:1\nz: 2\n"
	if string(annotated) != want {
		t.Fatalf("yaml =\n%s\nwant\n%s", annotated, want)
	}
}

func TestScalarPrintsCollectionsAsJSON(t *testing.T) {
	cases := map[string]any{"x": "x", "3": 3, "<nil>": nil, `["a"]`: []any{"a"}, `{"k":1}`: map[string]any{"k": 1}}
	for want, v := range cases {
		if got := Scalar(v); got != want {
			t.Errorf("Scalar(%#v) = %q, want %q", v, got, want)
		}
	}
}
