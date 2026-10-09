package ui

import (
	"context"
	"errors"
	"io"

	"github.com/charmbracelet/lipgloss"
)

// UI is what the command flows talk to; Plain prints line by line, Fullscreen renders a full-screen app.
type UI interface {
	// Ask shows the questions and returns ErrAborted when the user cancels.
	Ask(form Form) error
	Step(ctx context.Context, title string, fn func(context.Context) error) error
	// Begin marks a long phase without a spinner (e.g. while logs stream); call end when it finishes.
	Begin(title string) (end func(error))
	Logs() io.Writer
	Heading(msg string)
	Info(msg string)
	OK(msg string)
	Warn(msg string)
}

var (
	headingStyle = lipgloss.NewStyle().Bold(true)
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

var (
	// ErrQuit ends a flow without an error message.
	ErrQuit = errors.New("quit")
	// ErrAborted ends a flow because the user said no or cancelled a form.
	ErrAborted = errors.New("aborted")
)
