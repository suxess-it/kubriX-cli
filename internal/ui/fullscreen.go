package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Fullscreen runs a flow in a goroutine and renders it as a full-screen app.
// Flow calls are turned into messages for the bubbletea program.
type Fullscreen struct {
	p       *tea.Program
	quit    chan struct{}
	nextID  atomic.Int64
	logFile io.Writer

	mu      sync.Mutex
	summary []string
}

type (
	formMsg struct {
		form *huh.Form
		done chan error
	}
	stepStartMsg struct {
		id    int64
		title string
	}
	stepEndMsg struct {
		id  int64
		err error
	}
	textMsg     string
	flowDoneMsg struct{ err error }
)

// RunFullscreen runs flow full-screen. After the app closes, everything except the
// raw installer logs is printed to out, and the full transcript is kept at logPath.
func RunFullscreen(ctx context.Context, title string, out io.Writer, logPath string, flow func(context.Context, UI) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	f := &Fullscreen{quit: make(chan struct{}), logFile: io.Discard}
	if logPath != "" {
		lf, err := openLog(logPath)
		if err != nil {
			logPath = ""
		} else {
			defer func() { _ = lf.Close() }()
			f.logFile = lf
		}
	}

	// Detect the background color before bubbletea owns stdin; a later detection would race it for input.
	lipgloss.HasDarkBackground()
	f.p = tea.NewProgram(newModel(title, cancel), tea.WithAltScreen())
	flowDone := make(chan error, 1)
	go func() {
		err := flow(ctx, f)
		flowDone <- err
		f.p.Send(flowDoneMsg{err})
	}()

	_, runErr := f.p.Run()
	close(f.quit)
	cancel()

	var flowErr error
	select {
	case flowErr = <-flowDone:
	default:
		_, _ = fmt.Fprintln(out, "waiting for the current step to stop...")
		flowErr = <-flowDone
	}

	f.mu.Lock()
	for _, line := range f.summary {
		_, _ = fmt.Fprintln(out, line)
	}
	f.mu.Unlock()
	if logPath != "" {
		_, _ = fmt.Fprintln(out, dimStyle.Render("Full log: "+logPath))
	}
	if flowErr == nil {
		return runErr
	}
	return flowErr
}

func openLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

func (f *Fullscreen) Ask(form Form) error {
	done := make(chan error, 1)
	f.p.Send(formMsg{form: toHuh(form), done: done})
	select {
	case err := <-done:
		return errAborted(err)
	case <-f.quit:
		return ErrAborted
	}
}

func (f *Fullscreen) Step(ctx context.Context, title string, fn func(context.Context) error) error {
	end := f.Begin(title)
	err := fn(ctx)
	end(err)
	return err
}

func (f *Fullscreen) Begin(title string) func(error) {
	id := f.nextID.Add(1)
	f.p.Send(stepStartMsg{id: id, title: title})
	_, _ = fmt.Fprintln(f.logFile, "==> "+title)
	return func(err error) { f.p.Send(stepEndMsg{id: id, err: err}) }
}

type logWriter struct{ f *Fullscreen }

func (w logWriter) Write(p []byte) (int, error) {
	_, _ = w.f.logFile.Write(p)
	w.f.p.Send(textMsg(p))
	return len(p), nil
}

func (f *Fullscreen) Logs() io.Writer { return logWriter{f} }

func (f *Fullscreen) Heading(msg string) { f.line(msg, headingStyle.Render(msg)) }
func (f *Fullscreen) Info(msg string)    { f.line(msg, msg) }
func (f *Fullscreen) OK(msg string)      { f.line("✓ "+msg, okStyle.Render("✓ "+msg)) }
func (f *Fullscreen) Warn(msg string)    { f.line("! "+msg, warnStyle.Render("! "+msg)) }

func (f *Fullscreen) line(plain, styled string) {
	f.mu.Lock()
	f.summary = append(f.summary, styled)
	f.mu.Unlock()
	_, _ = fmt.Fprintln(f.logFile, plain)
	f.p.Send(textMsg(styled + "\n"))
}

const (
	sidebarWidth    = 34
	maxTranscriptKB = 256
)

type stepState int

const (
	stepRunning stepState = iota
	stepDone
	stepFailed
)

type step struct {
	id    int64
	title string
	state stepState
}

type model struct {
	title  string
	cancel context.CancelFunc
	start  time.Time

	width, height int
	steps         []step
	spin          spinner.Model
	vp            viewport.Model
	transcript    strings.Builder
	follow        bool

	form     *huh.Form
	formDone chan error

	cancelling bool
	finished   bool
	flowErr    error
	end        time.Time
}

func newModel(title string, cancel context.CancelFunc) *model {
	return &model{
		title:  title,
		cancel: cancel,
		start:  time.Now(),
		spin:   spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(okStyle)),
		follow: true,
	}
}

func (m *model) Init() tea.Cmd { return m.spin.Tick }

func (m *model) mainWidth() int  { return max(m.width-sidebarWidth-4, 20) }
func (m *model) bodyHeight() int { return max(m.height-4, 5) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width, m.vp.Height = m.mainWidth(), m.bodyHeight()
		if m.form != nil {
			m.form = m.form.WithWidth(m.mainWidth())
		}
		m.refresh()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case formMsg:
		m.form = msg.form.WithWidth(m.mainWidth()).WithShowHelp(true)
		m.formDone = msg.done
		return m, m.form.Init()

	case stepStartMsg:
		m.steps = append(m.steps, step{id: msg.id, title: msg.title})
		return m, nil

	case stepEndMsg:
		for i := range m.steps {
			if m.steps[i].id == msg.id {
				m.steps[i].state = stepDone
				if msg.err != nil {
					m.steps[i].state = stepFailed
				}
			}
		}
		return m, nil

	case textMsg:
		m.appendText(string(msg))
		return m, nil

	case flowDoneMsg:
		m.finished, m.flowErr, m.end = true, msg.err, time.Now()
		if errors.Is(msg.err, ErrQuit) || errors.Is(msg.err, ErrAborted) {
			return m, tea.Quit
		}
		return m, nil

	case tea.KeyMsg:
		if m.form != nil {
			break
		}
		switch {
		case m.finished && (msg.String() == "q" || msg.String() == "enter" || msg.String() == "esc" || msg.String() == "ctrl+c"):
			return m, tea.Quit
		case msg.String() == "ctrl+c" && m.cancelling:
			return m, tea.Quit
		case msg.String() == "ctrl+c":
			m.cancelling = true
			m.cancel()
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd
	}

	if m.form == nil {
		return m, nil
	}
	fm, cmd := m.form.Update(msg)
	m.form = fm.(*huh.Form)
	switch m.form.State {
	case huh.StateCompleted:
		m.formDone <- nil
		m.form = nil
	case huh.StateAborted:
		m.formDone <- huh.ErrUserAborted
		m.form = nil
	}
	return m, cmd
}

func (m *model) appendText(s string) {
	m.transcript.WriteString(strings.ReplaceAll(s, "\r", ""))
	if m.transcript.Len() > 2*maxTranscriptKB<<10 {
		keep := m.transcript.String()[m.transcript.Len()-maxTranscriptKB<<10:]
		if i := strings.IndexByte(keep, '\n'); i >= 0 {
			keep = keep[i+1:]
		}
		m.transcript.Reset()
		m.transcript.WriteString(keep)
	}
	m.refresh()
}

func (m *model) refresh() {
	if m.width == 0 {
		return
	}
	m.vp.SetContent(lipgloss.NewStyle().Width(m.mainWidth()).Render(m.transcript.String()))
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) View() string {
	if m.width == 0 {
		return "starting..."
	}
	now := time.Now()
	if m.finished {
		now = m.end
	}
	elapsed := now.Sub(m.start).Round(time.Second).String()
	header := headingStyle.Render(" kubriX · "+m.title) + dimStyle.Render("  "+elapsed)

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	sidebar := box.Width(sidebarWidth - 2).Height(m.bodyHeight()).Render(m.stepsView(sidebarWidth-2, m.bodyHeight()))

	main := m.vp.View()
	if m.form != nil {
		main = m.form.View()
	}
	mainBox := box.Width(m.mainWidth()).Height(m.bodyHeight()).MaxHeight(m.bodyHeight() + 2).Render(main)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		lipgloss.JoinHorizontal(lipgloss.Top, sidebar, mainBox),
		m.statusView(),
	)
}

func (m *model) stepsView(width, height int) string {
	lines := []string{headingStyle.Render("Steps")}
	for _, s := range m.steps {
		icon := strings.TrimSpace(m.spin.View())
		switch s.state {
		case stepDone:
			icon = okStyle.Render("✓")
		case stepFailed:
			icon = errStyle.Render("✗")
		}
		lines = append(lines, icon+" "+ansi.Truncate(strings.TrimSuffix(s.title, "..."), width-3, "…"))
	}
	if len(lines) > height {
		lines = append(lines[:1], lines[len(lines)-height+1:]...)
	}
	return strings.Join(lines, "\n")
}

func (m *model) statusView() string {
	switch {
	case m.form != nil:
		return dimStyle.Render(" answer the form · ctrl+c to abort")
	case m.finished && m.flowErr != nil:
		return errStyle.Render(" ✗ "+ansi.Truncate(m.flowErr.Error(), m.width-20, "…")) + dimStyle.Render(" · q to exit")
	case m.finished:
		return okStyle.Render(" ✓ done") + dimStyle.Render(" · ↑/↓ PgUp/PgDn scroll · q to exit")
	case m.cancelling:
		return warnStyle.Render(" cancelling... (ctrl+c again to quit immediately)")
	default:
		return dimStyle.Render(" ↑/↓ PgUp/PgDn scroll · ctrl+c cancel")
	}
}
