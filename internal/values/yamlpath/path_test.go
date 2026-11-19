package yamlpath

import (
	"reflect"
	"testing"
)

func TestJoinAndSplitAreInverse(t *testing.T) {
	cases := [][]string{{"a"}, {"a", "b"}, {"a.b", "c"}, {`c\d`, "e"}, {`x\.y`}, {"app.kubernetes.io/name"}}
	for _, parts := range cases {
		path := JoinPath(parts...)
		got, err := SplitPath(path)
		if err != nil || !reflect.DeepEqual(got, parts) {
			t.Errorf("SplitPath(JoinPath(%q)) = %q, %v (path %q)", parts, got, err, path)
		}
	}
}

func TestSplitRejectsEmptySegmentsAndTrailingEscapes(t *testing.T) {
	for _, p := range []string{"a..b", ".a", "a.", `a\`} {
		if _, err := SplitPath(p); err == nil {
			t.Errorf("SplitPath(%q) accepted", p)
		}
	}
}

func TestParseKeepsPositionsAndTreatsAnEmptyDocumentAsAnEmptyMap(t *testing.T) {
	doc, err := Parse("v.yaml", []byte("\n\na: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if k := doc.Root.Content[0]; k.Value != "a" || k.Line != 3 {
		t.Fatalf("key %q at line %d, want a at line 3", k.Value, k.Line)
	}
	empty, err := Parse("e.yaml", nil)
	if err != nil || len(empty.Root.Content) != 0 {
		t.Fatalf("empty document = %+v, %v", empty, err)
	}
}
