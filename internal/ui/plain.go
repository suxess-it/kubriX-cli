package ui

import (
	"context"
	"fmt"
	"io"

	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"
)

type Plain struct {
	out io.Writer
}

func NewPlain(out io.Writer) *Plain { return &Plain{out: out} }

func (p *Plain) Ask(form Form) error { return errAborted(toHuh(form).Run()) }

func (p *Plain) Step(ctx context.Context, title string, fn func(context.Context) error) error {
	return spinner.New().Title(title).Context(ctx).ActionWithErr(fn).Run()
}

func (p *Plain) Begin(title string) func(error) {
	p.Heading("---- " + title + " ----")
	return func(error) {}
}

func (p *Plain) Logs() io.Writer { return p.out }

// Styled lines go through lipgloss.Fprintln, which drops the colors a non-terminal or a dumb terminal can't show.
func (p *Plain) Heading(msg string) { _, _ = lipgloss.Fprintln(p.out, headingStyle.Render(msg)) }
func (p *Plain) Info(msg string)    { _, _ = fmt.Fprintln(p.out, msg) }
func (p *Plain) OK(msg string)      { _, _ = lipgloss.Fprintln(p.out, okStyle.Render("✓ "+msg)) }
func (p *Plain) Warn(msg string)    { _, _ = lipgloss.Fprintln(p.out, warnStyle.Render("! "+msg)) }
