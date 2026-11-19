package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/suxess-it/kubrix-cli/internal/gitopsdiff"
)

func key(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
}

func press(m *diffModel, keys ...string) {
	for _, k := range keys {
		drive(m, key(k), 3)
	}
}

func screen(m *diffModel) string { return ansi.Strip(m.View().Content) }

func file(chart, testCase, path string, status gitopsdiff.Status) gitopsdiff.FileDiff {
	return gitopsdiff.FileDiff{Chart: chart, TestCase: testCase, Path: path, Status: status, Additions: 1, Deletions: 1,
		Text: "diff -U 4 -rN target/" + path + " pr/" + path + "\n--- a\n+++ b\n@@ -1 +1 @@\n-old " + path + "\n+new " + path + "\n"}
}

func report() *gitopsdiff.Report {
	return &gitopsdiff.Report{
		Files: []gitopsdiff.FileDiff{
			file("grafana", "kind", "templates/cm.yaml", gitopsdiff.Modified),
			file("grafana", "kind", "templates/ingress.yaml", gitopsdiff.Removed),
			file("loki", "kind", "templates/sts.yaml", gitopsdiff.Added),
		},
		ValueChanges:      []gitopsdiff.ValueChange{{Chart: "grafana", TestCase: "kind", Path: "grafana.replicas", Kind: gitopsdiff.Modified, Before: 1, After: 2}},
		DefaultValuesDiff: "-a: 1\n+a: 2\n",
		Failures:          []gitopsdiff.Failure{{Chart: "keycloak", TestCase: "kind-base", Status: gitopsdiff.StillBroken, Error: "wrong type for value"}},
	}
}

// browsing is a viewer showing report and helm's warnings, as after a run against main.
func browsing(t *testing.T, r *gitopsdiff.Report, warnings string) *diffModel {
	t.Helper()
	m := newDiffModel(DiffSession{Base: "main", Head: "working tree", Run: func(context.Context, DiffSelection, func(gitopsdiff.Progress)) (*gitopsdiff.Report, error) {
		return r, nil
	}, Warnings: func() string { return warnings }}, func() {})
	drive(m, tea.WindowSizeMsg{Width: 140, Height: 30}, 1)
	runCmd(m, m.startRun(), 2)
	if m.phase != phaseBrowsing {
		t.Fatalf("phase = %v, want browsing", m.phase)
	}
	return m
}

func TestTheTreeListsChartsTestCasesFilesFailuresValuesAndWarnings(t *testing.T) {
	m := browsing(t, report(), "warning: a\nwarning: b")
	view := screen(m)
	for _, want := range []string{
		"kubriX · diff main … working tree",
		"▾ grafana  +2/-2", "  ▸ kind  +2/-2", "▸ loki  +1/-1",
		"! keycloak · kind-base", "● Changed values (1)", "● Default values", "● Helm warnings (2)",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if !m.View().AltScreen {
		t.Error("the viewer must use the alternate screen")
	}
	checkLayout(t, view, 140, 30)
}

func TestNavigatingOpensFoldsAndShowsTheSelectedDiff(t *testing.T) {
	m := browsing(t, report(), "warning: a\nwarning: b")
	press(m, "down", "enter") // grafana's test case kind
	if v := screen(m); !strings.Contains(v, "▾ kind") || !strings.Contains(v, "~ templates/cm.yaml") || !strings.Contains(v, "- templates/ingress.yaml") {
		t.Fatalf("test case not expanded:\n%s", v)
	}
	press(m, "down", "down")
	if v := screen(m); !strings.Contains(v, "-old templates/ingress.yaml") || strings.Contains(v, "-old templates/cm.yaml") {
		t.Fatalf("the diff pane must show the selected file only:\n%s", v)
	}
	press(m, "left") // to the parent
	if v := screen(m); !strings.Contains(v, "-old templates/cm.yaml") || !strings.Contains(v, "-old templates/ingress.yaml") {
		t.Fatalf("a test case shows all its files:\n%s", v)
	}
	press(m, "left") // fold it
	if v := screen(m); strings.Contains(v, "~ templates/cm.yaml") {
		t.Fatalf("test case not folded:\n%s", v)
	}
}

func TestNextAndPreviousFileJumpAcrossCharts(t *testing.T) {
	m := browsing(t, report(), "warning: a\nwarning: b")
	var seen []string
	for range 4 {
		press(m, "n")
		seen = append(seen, m.shown.label)
	}
	want := []string{"templates/cm.yaml", "templates/ingress.yaml", "templates/sts.yaml", "templates/cm.yaml"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("n visits %v, want %v (wrapping around)", seen, want)
	}
	press(m, "p")
	if m.shown.label != "templates/sts.yaml" || !strings.Contains(screen(m), "▾ loki") {
		t.Fatalf("p went to %q; loki must be opened:\n%s", m.shown.label, screen(m))
	}
}

func TestTheFilterKeepsMatchingNodesWithTheirParents(t *testing.T) {
	m := browsing(t, report(), "warning: a\nwarning: b")
	press(m, "/", "s", "t", "s", "enter")
	view := screen(m)
	if !strings.Contains(view, "/sts") || !strings.Contains(view, "loki") || !strings.Contains(view, "+ templates/sts.yaml") || strings.Contains(view, "grafana") || strings.Contains(view, "keycloak") {
		t.Fatalf("filtered view:\n%s", view)
	}
	press(m, "esc")
	if !strings.Contains(screen(m), "grafana") {
		t.Fatalf("esc must clear the filter:\n%s", screen(m))
	}
}

func TestFailuresValuesAndWarningsShowTheirDetails(t *testing.T) {
	m := browsing(t, report(), "warning: a\nwarning: b")
	cases := map[string]string{
		"keycloak · kind-base": "already broken on the base",
		"Changed values (1)":   "grafana.replicas  1       2",
		"Default values":       "+a: 2",
		"Helm warnings (2)":    "warning: b",
	}
	for label, want := range cases {
		for i, n := range m.visible() {
			if n.label == label {
				m.cursor = i
				m.showSelected(true)
			}
		}
		if !strings.Contains(screen(m), want) {
			t.Errorf("%s: view lacks %q:\n%s", label, want, screen(m))
		}
	}
}

func TestTabMovesKeysToTheDiffPane(t *testing.T) {
	m := browsing(t, report(), "warning: a\nwarning: b")
	press(m, "tab", "down")
	if m.cursor != 0 || !m.focusDiff {
		t.Fatalf("with the diff focused, down scrolls the diff, not the tree (cursor %d)", m.cursor)
	}
	press(m, "tab", "down")
	if m.cursor != 1 {
		t.Fatalf("tab must return to the tree (cursor %d)", m.cursor)
	}
}

func TestAnEmptyReportSaysSo(t *testing.T) {
	m := browsing(t, &gitopsdiff.Report{}, "")
	if !strings.Contains(screen(m), "No changes in the rendered manifests") {
		t.Fatalf("view:\n%s", screen(m))
	}
}

func TestTheBaseIsAskedForFirstAndThenRendered(t *testing.T) {
	var gotBase string
	var progress []string
	m := newDiffModel(DiffSession{Bases: []string{"origin/main", "dev"}, Head: "working tree",
		Run: func(_ context.Context, sel DiffSelection, p func(gitopsdiff.Progress)) (*gitopsdiff.Report, error) {
			gotBase = sel.Base
			p(gitopsdiff.Progress{Done: 1, Total: 2, Last: "grafana · kind (pr)"})
			return &gitopsdiff.Report{}, nil
		}}, func() {})
	m.send = func(msg tea.Msg) {
		if p, ok := msg.(progressMsg); ok {
			progress = append(progress, p.Last)
		}
	}
	drive(m, tea.WindowSizeMsg{Width: 140, Height: 30}, 1)
	runCmd(m, m.form.Init(), 2)
	if v := screen(m); !strings.Contains(v, "Compare against which base?") || !strings.Contains(v, "origin/main") || !strings.Contains(v, "other…") {
		t.Fatalf("base form:\n%s", v)
	}
	press(m, "down", "enter")
	for range 5 {
		if m.phase == phaseBrowsing {
			break
		}
		drive(m, nil, 3)
	}
	if gotBase != "dev" || len(progress) != 1 || m.phase != phaseBrowsing {
		t.Fatalf("base %q, progress %v, phase %v", gotBase, progress, m.phase)
	}
}

func TestATypedBaseIsUsedWhenOtherIsChosen(t *testing.T) {
	var gotBase string
	m := newDiffModel(DiffSession{Bases: []string{"main"}, Head: "working tree",
		Run: func(_ context.Context, sel DiffSelection, _ func(gitopsdiff.Progress)) (*gitopsdiff.Report, error) {
			gotBase = sel.Base
			return &gitopsdiff.Report{}, nil
		}}, func() {})
	drive(m, tea.WindowSizeMsg{Width: 140, Height: 30}, 1)
	runCmd(m, m.form.Init(), 2)
	press(m, "down", "enter", "v", "1", "enter")
	for range 5 {
		if m.phase == phaseBrowsing {
			break
		}
		drive(m, nil, 3)
	}
	if gotBase != "v1" {
		t.Fatalf("base = %q, want v1", gotBase)
	}
}

func TestARunErrorIsShownAndQuittingCancels(t *testing.T) {
	cancelled := false
	m := newDiffModel(DiffSession{Base: "main", Head: "working tree", Run: func(context.Context, DiffSelection, func(gitopsdiff.Progress)) (*gitopsdiff.Report, error) {
		return nil, errors.New("check out main: bad revision")
	}}, func() { cancelled = true })
	drive(m, tea.WindowSizeMsg{Width: 140, Height: 30}, 1)
	runCmd(m, m.startRun(), 2)
	if !strings.Contains(screen(m), "✗ check out main: bad revision") {
		t.Fatalf("view:\n%s", screen(m))
	}
	if _, cmd := m.Update(key("q")); cmd == nil || !cancelled {
		t.Fatal("q must cancel and quit")
	}
}

// selecting is a viewer with a base whose choices are grafana, loki and tempo for the target types kind and peak,
// grafana changed; runs records the selections rendered.
func selecting(t *testing.T, runs *[]DiffSelection) *diffModel {
	t.Helper()
	m := newDiffModel(DiffSession{Base: "main", Head: "working tree",
		Choices: func(_ context.Context, base string) (DiffChoices, error) {
			if base != "main" {
				t.Errorf("choices for base %q", base)
			}
			return DiffChoices{
				TestCases: []Option{{Label: "kind", Value: "kind", Selected: true}, {Label: "peak", Value: "peak", Selected: true}},
				Charts:    []Option{{Label: "grafana (changed)", Value: "grafana", Selected: true}, {Label: "loki", Value: "loki"}, {Label: "tempo", Value: "tempo"}},
			}, nil
		},
		Run: func(_ context.Context, sel DiffSelection, _ func(gitopsdiff.Progress)) (*gitopsdiff.Report, error) {
			*runs = append(*runs, sel)
			return report(), nil
		}}, func() {})
	drive(m, tea.WindowSizeMsg{Width: 140, Height: 30}, 1)
	if v := screen(m); !strings.Contains(v, "finding the target types and charts") {
		t.Fatalf("with a base the viewer lists the choices first:\n%s", v)
	}
	runCmd(m, m.afterBase(), 3)
	return m
}

func finishForm(m *diffModel) {
	for range 6 {
		if m.form == nil {
			return
		}
		press(m, "enter")
	}
}

func TestTheSelectionOffersTestCasesAndChartsWithTheDefaultsSelected(t *testing.T) {
	var runs []DiffSelection
	m := selecting(t, &runs)
	if v := screen(m); m.phase != phaseSelect || !strings.Contains(v, "Render with which test cases?") || !strings.Contains(v, "peak") {
		t.Fatalf("test case page:\n%s", v)
	}
	press(m, "down", "space") // deselect peak
	press(m, "enter")
	if v := screen(m); !strings.Contains(v, "Render which charts?") || !strings.Contains(v, "grafana (changed)") || !strings.Contains(v, "tempo") {
		t.Fatalf("chart page:\n%s", v)
	}
	press(m, "down", "space") // add loki
	finishForm(m)
	for range 5 {
		if m.phase == phaseBrowsing {
			break
		}
		drive(m, nil, 3)
	}
	want := DiffSelection{Base: "main", TestCases: []string{"kind"}, Charts: []string{"grafana", "loki"}}
	if len(runs) != 1 || runs[0].Base != want.Base || strings.Join(runs[0].TestCases, ",") != "kind" || strings.Join(runs[0].Charts, ",") != "grafana,loki" {
		t.Fatalf("rendered %+v, want %+v", runs, want)
	}
}

func TestReselectingFromTheResultRendersAgainOrGoesBack(t *testing.T) {
	var runs []DiffSelection
	m := selecting(t, &runs)
	finishForm(m)
	for range 5 {
		if m.phase == phaseBrowsing {
			break
		}
		drive(m, nil, 3)
	}
	press(m, "r")
	if m.phase != phaseSelect || !strings.Contains(screen(m), "esc back to the result") {
		t.Fatalf("r must reopen the selection:\n%s", screen(m))
	}
	press(m, "esc")
	if m.phase != phaseBrowsing || len(runs) != 1 {
		t.Fatalf("esc must return to the result without rendering (phase %v, runs %d)", m.phase, len(runs))
	}
	press(m, "r")
	finishForm(m)
	for range 5 {
		if m.phase == phaseBrowsing {
			break
		}
		drive(m, nil, 3)
	}
	if len(runs) != 2 || strings.Join(runs[1].TestCases, ",") != "kind,peak" {
		t.Fatalf("a reselection renders again with the kept selection: %+v", runs)
	}
}

func TestAChoicesErrorIsShown(t *testing.T) {
	m := newDiffModel(DiffSession{Base: "nope", Head: "working tree",
		Choices: func(context.Context, string) (DiffChoices, error) {
			return DiffChoices{}, errors.New("list nope: bad revision")
		}}, func() {})
	drive(m, tea.WindowSizeMsg{Width: 140, Height: 30}, 1)
	runCmd(m, m.afterBase(), 2)
	if !strings.Contains(screen(m), "✗ list nope: bad revision") {
		t.Fatalf("view:\n%s", screen(m))
	}
}
