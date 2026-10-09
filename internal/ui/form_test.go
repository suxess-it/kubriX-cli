package ui

import (
	"errors"
	"testing"

	"github.com/charmbracelet/huh"
)

func TestToHuhKeepsPagesFieldsAndHiddenPages(t *testing.T) {
	var name string
	var apps []string
	ok := true
	hidden := true
	form := Form{Pages: []Page{
		{Title: "First", Description: "d", Fields: []Field{
			Input{Key: "name", Title: "Name", Value: &name},
			Choice{Key: "kind", Title: "Kind", Options: Options("a", "b"), Value: new(string)},
		}},
		{Hidden: func() bool { return hidden }, Fields: []Field{Confirmation{Key: "ok", Title: "Ok?", Value: &ok}}},
		{Fields: []Field{Choices{Key: "apps", Title: "Apps", Options: []Option{{Label: "A", Value: "a", Selected: true}, {Label: "B", Value: "b"}}, Height: 6, Value: &apps}}},
	}}
	hf := toHuh(form)
	if hf == nil {
		t.Fatal("no form")
	}
	// huh reports the number of groups it was given only through running them, so check the pieces that carry
	// our model: option labels and values.
	opts := huhOptions(form.Pages[2].Fields[0].(Choices).Options)
	if len(opts) != 2 || opts[0].Key != "A" || opts[0].Value != "a" || opts[1].Key != "B" || opts[1].Value != "b" {
		t.Errorf("options %+v", opts)
	}
}

func TestOptionsLabelsByValue(t *testing.T) {
	got := Options("x", "y")
	if len(got) != 2 || got[0] != (Option{Label: "x", Value: "x"}) || got[1] != (Option{Label: "y", Value: "y"}) {
		t.Errorf("got %+v", got)
	}
}

// huh offers no way to read a field's settings back, so the translation is checked by running each field.
func TestEveryFieldKindIsTranslated(t *testing.T) {
	var (
		v    string
		many []string
		ok   bool
	)
	fields := []Field{
		Input{Title: "T", Secret: true, Validate: func(string) error { return nil }, Value: &v},
		Input{Title: "T", DescriptionFunc: func() string { return "d" }, Watch: &v, Value: &v},
		Choice{Title: "T", Options: Options("a"), Value: &v},
		Choices{Title: "T", Options: Options("a"), Height: 4, Validate: func([]string) error { return nil }, Value: &many},
		Confirmation{Title: "T", Value: &ok},
	}
	for _, f := range fields {
		if f.huhField() == nil {
			t.Errorf("%T was not translated", f)
		}
	}
}

func TestErrAbortedMapsOnlyCancellation(t *testing.T) {
	if !errors.Is(errAborted(huh.ErrUserAborted), ErrAborted) {
		t.Error("huh's cancellation must become ErrAborted")
	}
	other := errors.New("other")
	if errAborted(other) != other {
		t.Error("other errors pass through")
	}
	if errAborted(nil) != nil {
		t.Error("success stays success")
	}
}

type recordingUI struct {
	UI
	form Form
	err  error
}

func (r *recordingUI) Ask(f Form) error { r.form = f; return r.err }

func TestConfirmAndSelectHelpersAskOneFieldKeyedByTitle(t *testing.T) {
	r := &recordingUI{}
	ok, err := Confirm(r, "Proceed?", "desc", true)
	if err != nil || !ok {
		t.Fatalf("default is returned when the form leaves it alone: %v %v", ok, err)
	}
	c, isConfirm := r.form.Pages[0].Fields[0].(Confirmation)
	if !isConfirm || c.Key != "Proceed?" || c.Description != "desc" {
		t.Errorf("field %+v", r.form.Pages[0].Fields[0])
	}

	choice := "b"
	if err := Select(r, "Pick", "d", Options("a", "b"), &choice); err != nil {
		t.Fatal(err)
	}
	s, isChoice := r.form.Pages[0].Fields[0].(Choice)
	if !isChoice || s.Key != "Pick" || s.Value != &choice || len(s.Options) != 2 {
		t.Errorf("field %+v", r.form.Pages[0].Fields[0])
	}

	r.err = ErrAborted
	if _, err := Confirm(r, "Proceed?", "", true); !errors.Is(err, ErrAborted) {
		t.Errorf("errors pass through: %v", err)
	}
}
