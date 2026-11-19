package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
)

// drive feeds msg to the model and then the messages its commands produce, like bubbletea would.
// Blink/tick commands that never finish on their own are cut off by the depth limit.
func drive(m tea.Model, msg tea.Msg, depth int) {
	_, cmd := m.Update(msg)
	runCmd(m, cmd, depth)
}

func runCmd(m tea.Model, cmd tea.Cmd, depth int) {
	if cmd == nil || depth == 0 {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			runCmd(m, c, depth-1)
		}
	case tea.QuitMsg:
	default:
		drive(m, msg, depth-1)
	}
}

// checkLayout asserts a full-screen view fills the terminal exactly: as many lines as rows, no line wider than the
// terminal, and side-by-side boxes of the same height (their bottom borders on one line).
func checkLayout(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Errorf("view has %d lines, terminal has %d:\n%s", len(lines), height, view)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > width {
			t.Errorf("line %d is %d wide, terminal is %d", i, w, width)
		}
	}
	if bottom := lines[len(lines)-2]; strings.Count(bottom, "╰") != 2 {
		t.Errorf("the boxes differ in height; second to last line: %q\n%s", bottom, view)
	}
}

func TestModelRendersStepsAndTranscript(t *testing.T) {
	m := newModel("kind demo", func() {})
	drive(m, tea.WindowSizeMsg{Width: 120, Height: 30}, 1)
	drive(m, stepStartMsg{id: 1, title: "Creating kind cluster kubrix-demo"}, 1)
	drive(m, stepEndMsg{id: 1}, 1)
	drive(m, stepStartMsg{id: 2, title: "Installing kubriX"}, 1)
	drive(m, stepEndMsg{id: 2, err: errors.New("boom")}, 1)
	drive(m, textMsg("installer says hello\r\n"), 1)

	if !m.View().AltScreen {
		t.Fatal("the full-screen app must use the alternate screen")
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"kubriX · kind demo", "Steps", "✓ Creating kind cluster", "✗ Installing kubriX", "installer says hello", "ctrl+c cancel"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	checkLayout(t, view, 120, 30)
}

func TestModelFormRoundTrip(t *testing.T) {
	m := newModel("kind demo", func() {})
	drive(m, tea.WindowSizeMsg{Width: 120, Height: 30}, 1)

	ok := false
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Proceed?").Affirmative("Yes").Negative("No").Value(&ok)))
	done := make(chan error, 1)
	drive(m, formMsg{form: form, done: done}, 3)
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Proceed?") {
		t.Fatalf("form not shown:\n%s", view)
	}
	drive(m, tea.KeyPressMsg{Code: tea.KeyLeft}, 3)
	drive(m, tea.KeyPressMsg{Code: tea.KeyEnter}, 5)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("form did not complete")
	}
	if !ok {
		t.Error("expected Yes to be selected")
	}
	if m.form != nil {
		t.Error("completed form must be removed from the main panel")
	}
}

func TestModelFormAbort(t *testing.T) {
	m := newModel("kind demo", func() {})
	drive(m, tea.WindowSizeMsg{Width: 120, Height: 30}, 1)
	done := make(chan error, 1)
	drive(m, formMsg{form: huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Proceed?"))), done: done}, 3)
	drive(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, 3)
	if err := <-done; !errors.Is(err, huh.ErrUserAborted) {
		t.Fatalf("got %v, want ErrUserAborted", err)
	}
}

func TestModelCancelThenQuit(t *testing.T) {
	cancelled := false
	m := newModel("kind demo", func() { cancelled = true })
	drive(m, tea.WindowSizeMsg{Width: 120, Height: 30}, 1)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd != nil || !cancelled {
		t.Fatal("first ctrl+c must cancel the flow, not quit")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "cancelling") {
		t.Error("status should say cancelling")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil {
		t.Fatal("second ctrl+c must quit")
	}
}

func TestModelQuitsImmediatelyOnErrQuit(t *testing.T) {
	m := newModel("kubriX", func() {})
	if _, cmd := m.Update(flowDoneMsg{err: ErrQuit}); cmd == nil {
		t.Fatal("ErrQuit must quit without waiting for a key")
	}
	m = newModel("kubriX", func() {})
	if _, cmd := m.Update(flowDoneMsg{err: nil}); cmd != nil {
		t.Fatal("a finished flow waits for the user to read the result")
	}
}
