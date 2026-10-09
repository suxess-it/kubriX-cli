package cmd

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
	"github.com/suxess-it/kubrix-cli/internal/ui/uitest"
)

func TestFeaturesFromEnv(t *testing.T) {
	env := func(value string) func(string) string {
		return func(key string) string {
			if key != experimentalEnv {
				t.Errorf("unexpected variable %q", key)
			}
			return value
		}
	}
	for value, want := range map[string]bool{
		"": false, "0": false, "false": false, "no": false, "off": false, "nonsense": false,
		"1": true, "true": true, "TRUE": true, " yes ": true, "on": true,
	} {
		if got := FeaturesFromEnv(env(value)).Experimental; got != want {
			t.Errorf("%s=%q: got %v, want %v", experimentalEnv, value, got, want)
		}
	}
}

func help(t *testing.T, f Features, args ...string) string {
	t.Helper()
	root := NewRoot(BuildInfo{}, f)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestExperimentalCommandsAreHiddenUnlessEnabled(t *testing.T) {
	off := help(t, Features{}, "--help")
	for _, shown := range []string{"demo"} {
		if !strings.Contains(off, shown) {
			t.Errorf("%q must be available:\n%s", shown, off)
		}
	}
	for _, hidden := range []string{"install", "upgrade"} {
		if strings.Contains(off, hidden+" ") {
			t.Errorf("%q must be hidden without the feature:\n%s", hidden, off)
		}
	}
	on := help(t, Features{Experimental: true}, "--help")
	for _, shown := range []string{"demo", "install", "upgrade", "(experimental)"} {
		if !strings.Contains(on, shown) {
			t.Errorf("%q must be listed with the feature:\n%s", shown, on)
		}
	}
	if sub := help(t, Features{}, "demo", "--help"); !strings.Contains(sub, "delete") {
		t.Errorf("demo delete is always available:\n%s", sub)
	}
}

func TestExperimentalCommandsRefuseToRunUnlessEnabled(t *testing.T) {
	for _, command := range []string{"install", "upgrade"} {
		root := NewRoot(BuildInfo{}, Features{})
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{command})
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "experimental") || !strings.Contains(err.Error(), experimentalEnv+"=1") {
			t.Errorf("%s: %v", command, err)
		}
	}
}

func TestMenuOffersOnlyTheDemoWithoutTheFeature(t *testing.T) {
	const ask = "What do you want to do?"
	for _, choice := range []string{"demo", "delete", "help", "quit"} {
		if got, err := menu(uitest.New(map[string]any{ask: choice}), catalogWith(t), Features{}); err != nil || got != choice {
			t.Errorf("%s: %q %v", choice, got, err)
		}
	}
	for _, choice := range []string{"install", "upgrade"} {
		var rejected *uitest.AnswerError
		if _, err := menu(uitest.New(map[string]any{ask: choice}), catalogWith(t), Features{}); !errors.As(err, &rejected) {
			t.Errorf("%s must not be offered: %v", choice, err)
		}
		if got, err := menu(uitest.New(map[string]any{ask: choice}), catalogWith(t), Features{Experimental: true}); err != nil || got != choice {
			t.Errorf("%s must be offered with the feature: %q %v", choice, got, err)
		}
	}
}

func TestMenuCountsOnlyWhatTheUserCanSee(t *testing.T) {
	saved := catalogWith(t,
		state.Installation{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo"},
		state.Installation{Kind: state.Cluster, Context: "prod", Org: "acme", Repo: "platform"},
	)
	describe := func(f Features) string {
		t.Helper()
		var description string
		spy := &describingUI{UI: uitest.New(nil), description: &description}
		if _, err := menu(spy, saved, f); err != nil {
			t.Fatal(err)
		}
		return description
	}
	if got := describe(Features{}); got != "Saved installations: 1 (last: acme/demo on kind cluster kubrix-demo)" {
		t.Errorf("without the feature: %q", got)
	}
	if got := describe(Features{Experimental: true}); got != "Saved installations: 2 (last: acme/platform on context prod)" {
		t.Errorf("with the feature: %q", got)
	}
}

// describingUI records the description of the first question asked.
type describingUI struct {
	ui.UI
	description *string
}

func (d *describingUI) Ask(f ui.Form) error {
	if c, ok := f.Pages[0].Fields[0].(ui.Choice); ok {
		*d.description = c.Description
	}
	return d.UI.Ask(f)
}

func TestRunDemoDeleteOnlyKnowsDemosWithoutTheFeature(t *testing.T) {
	w := newWorld(t)
	w.svc.experimental = false
	w.kind.Existing = true
	rec := w.catalog()
	for _, st := range []state.Installation{
		{Kind: state.KindDemo, ClusterName: "kubrix-demo", Org: "acme", Repo: "demo"},
		{Kind: state.Cluster, Context: "prod", Org: "acme", Repo: "platform"},
	} {
		if err := rec.Record(st); err != nil {
			t.Fatal(err)
		}
	}
	u := uitest.New(nil)
	if err := runDemoDelete(context.Background(), u, w.svc); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(u.Asked(), "Which installation?") {
		t.Error("with a single demo there is nothing to choose between")
	}
	if !slices.Equal(w.kind.Calls, []string{"delete kubrix-demo"}) {
		t.Errorf("kind %v", w.kind.Calls)
	}
	if _, ok := w.catalog().Find("prod"); !ok {
		t.Error("an installation on an existing cluster is not the demo's business")
	}

	err := runDemoDelete(context.Background(), uitest.New(nil), w.svc)
	if err == nil || !strings.Contains(err.Error(), "nothing to delete") {
		t.Errorf("only an install is left, which is hidden: %v", err)
	}
}

func TestDemoItselfDoesNotNeedTheFeature(t *testing.T) {
	w := newWorld(t)
	w.svc.experimental = false
	w.emptyRepoAt("demo")
	if err := runDemo(context.Background(), uitest.New(demoAnswers(nil)), w.svc); err != nil {
		t.Fatal(err)
	}
	if saved, ok := w.catalog().Find("kubrix-demo"); !ok || saved.GitHost != git.GitHubName {
		t.Errorf("saved %+v", saved)
	}
}
