package uitest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/suxess-it/kubrix-cli/internal/ui"
)

func ask(u *UI, fields ...ui.Field) error {
	return u.Ask(ui.Form{Pages: []ui.Page{{Fields: fields}}})
}

func TestUnansweredFieldsKeepTheirDefaults(t *testing.T) {
	name, kind, ok := "default", "b", true
	apps := []string{"x"}
	u := New(nil)
	err := ask(u,
		ui.Input{Key: "name", Value: &name},
		ui.Choice{Key: "kind", Options: ui.Options("a", "b"), Value: &kind},
		ui.Choices{Key: "apps", Options: ui.Options("x", "y"), Value: &apps},
		ui.Confirmation{Key: "ok", Value: &ok},
	)
	if err != nil || name != "default" || kind != "b" || !slices.Equal(apps, []string{"x"}) || !ok {
		t.Errorf("%v %q %q %v %v", err, name, kind, apps, ok)
	}
	if !slices.Equal(u.Asked(), []string{"name", "kind", "apps", "ok"}) || u.Forms() != 1 {
		t.Errorf("asked %v forms %d", u.Asked(), u.Forms())
	}
}

func TestAnswersAreWrittenThroughThePointers(t *testing.T) {
	name, kind, ok := "", "a", false
	var apps []string
	u := New(map[string]any{"name": "n", "kind": "b", "apps": []string{"y"}, "ok": true})
	err := ask(u,
		ui.Input{Key: "name", Value: &name},
		ui.Choice{Key: "kind", Options: ui.Options("a", "b"), Value: &kind},
		ui.Choices{Key: "apps", Options: ui.Options("x", "y"), Value: &apps},
		ui.Confirmation{Key: "ok", Value: &ok},
	)
	if err != nil || name != "n" || kind != "b" || !slices.Equal(apps, []string{"y"}) || !ok {
		t.Errorf("%v %q %q %v %v", err, name, kind, apps, ok)
	}
	if len(u.Unused()) != 0 {
		t.Errorf("unused %v", u.Unused())
	}
}

func TestUnusedAnswersAreReported(t *testing.T) {
	name := ""
	u := New(map[string]any{"name": "n", "typo": "x", "another": 1})
	if err := ask(u, ui.Input{Key: "name", Value: &name}); err != nil {
		t.Fatal(err)
	}
	if got := u.Unused(); !slices.Equal(got, []string{"another", "typo"}) {
		t.Errorf("unused %v", got)
	}
}

func TestValidatorsRejectAnswersLikeARealForm(t *testing.T) {
	name := ""
	rejects := func(string) error { return errors.New("bad") }
	err := ask(New(map[string]any{"name": "x"}), ui.Input{Key: "name", Validate: rejects, Value: &name})
	var rejected *AnswerError
	if !errors.As(err, &rejected) || rejected.Key != "name" || !strings.Contains(err.Error(), "bad") {
		t.Errorf("got %v", err)
	}
	// The default is validated too, because pressing enter submits it.
	err = ask(New(nil), ui.Input{Key: "name", Validate: rejects, Value: &name})
	if !errors.As(err, &rejected) {
		t.Errorf("got %v", err)
	}
	apps := []string{}
	err = ask(New(map[string]any{"apps": []string{"x"}}), ui.Choices{Key: "apps", Options: ui.Options("x"), Validate: func([]string) error { return errors.New("dependency") }, Value: &apps})
	if !errors.As(err, &rejected) {
		t.Errorf("got %v", err)
	}
}

func TestChoicesOnlyAcceptTheirOptions(t *testing.T) {
	kind := "a"
	var rejected *AnswerError
	if err := ask(New(map[string]any{"kind": "z"}), ui.Choice{Key: "kind", Options: ui.Options("a", "b"), Value: &kind}); !errors.As(err, &rejected) {
		t.Errorf("got %v", err)
	}
	var apps []string
	if err := ask(New(map[string]any{"apps": []string{"z"}}), ui.Choices{Key: "apps", Options: ui.Options("x"), Value: &apps}); !errors.As(err, &rejected) {
		t.Errorf("got %v", err)
	}
}

// Like huh, a Choice whose default is not an option starts on the first option.
func TestChoiceWithoutAMatchingDefaultStartsOnTheFirstOption(t *testing.T) {
	kind := ""
	if err := ask(New(nil), ui.Choice{Key: "kind", Options: ui.Options("a", "b"), Value: &kind}); err != nil || kind != "a" {
		t.Errorf("%v %q", err, kind)
	}
}

func TestChoicesStartWithTheSelectedOptions(t *testing.T) {
	var apps []string
	opts := []ui.Option{{Label: "X", Value: "x", Selected: true}, {Label: "Y", Value: "y"}}
	if err := ask(New(nil), ui.Choices{Key: "apps", Options: opts, Value: &apps}); err != nil || !slices.Equal(apps, []string{"x"}) {
		t.Errorf("%v %v", err, apps)
	}
}

func TestWrongAnswerTypesAreReported(t *testing.T) {
	name, ok := "", false
	var rejected *AnswerError
	if err := ask(New(map[string]any{"name": 1}), ui.Input{Key: "name", Value: &name}); !errors.As(err, &rejected) {
		t.Errorf("got %v", err)
	}
	if err := ask(New(map[string]any{"ok": "yes"}), ui.Confirmation{Key: "ok", Value: &ok}); !errors.As(err, &rejected) {
		t.Errorf("got %v", err)
	}
}

func TestAbortCancelsTheForm(t *testing.T) {
	name := ""
	err := ask(New(map[string]any{"name": Abort}), ui.Input{Key: "name", Value: &name})
	if !errors.Is(err, ui.ErrAborted) {
		t.Errorf("got %v", err)
	}
}

func TestHiddenPagesAreSkippedAndSeeEarlierAnswers(t *testing.T) {
	choice, extra := "no", ""
	u := New(map[string]any{"choice": "yes", "extra": "e"})
	err := u.Ask(ui.Form{Pages: []ui.Page{
		{Fields: []ui.Field{ui.Choice{Key: "choice", Options: ui.Options("yes", "no"), Value: &choice}}},
		{Hidden: func() bool { return choice != "yes" }, Fields: []ui.Field{ui.Input{Key: "extra", Value: &extra}}},
	}})
	if err != nil || extra != "e" {
		t.Fatalf("%v %q", err, extra)
	}

	choice, extra = "no", ""
	hidden := New(map[string]any{"choice": "no"})
	err = hidden.Ask(ui.Form{Pages: []ui.Page{
		{Fields: []ui.Field{ui.Choice{Key: "choice", Options: ui.Options("yes", "no"), Value: &choice}}},
		{Hidden: func() bool { return choice != "yes" }, Fields: []ui.Field{ui.Input{Key: "extra", Value: &extra}}},
	}})
	if err != nil || !slices.Equal(hidden.Asked(), []string{"choice"}) {
		t.Errorf("%v asked %v", err, hidden.Asked())
	}
}

func TestRecordsWhatTheFlowShows(t *testing.T) {
	u := New(nil)
	u.Heading("h")
	u.Info("i")
	u.OK("o")
	u.Warn("w")
	ran := false
	if err := u.Step(context.Background(), "step", func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatal(err)
	}
	u.Begin("phase")(nil)
	if !slices.Equal(u.Headings(), []string{"h"}) || !slices.Equal(u.Infos(), []string{"i"}) || !slices.Equal(u.OKs(), []string{"o"}) ||
		!slices.Equal(u.Warns(), []string{"w"}) || !slices.Equal(u.Steps(), []string{"step"}) {
		t.Errorf("%+v", u)
	}
}
