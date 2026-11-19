package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/suxess-it/kubrix-cli/internal/gitopsdiff"
	"github.com/suxess-it/kubrix-cli/internal/values/output"
)

// DiffSession is what the diff viewer needs: the base to compare against (empty asks for one among Bases), the
// revision being compared, what can be rendered, and the diff itself.
type DiffSession struct {
	Base  string
	Bases []string
	Head  string
	// Choices lists the test cases and charts that can be rendered against base, the default selection selected;
	// nil renders the defaults without asking.
	Choices func(ctx context.Context, base string) (DiffChoices, error)
	// Run computes the diff of the selection, reporting progress as it goes.
	Run func(ctx context.Context, sel DiffSelection, progress func(gitopsdiff.Progress)) (*gitopsdiff.Report, error)
	// Warnings returns what helm warned about while rendering, once Run returned.
	Warnings func() string
}

// DiffChoices are the test cases and charts offered for a diff; Selected marks the default selection.
type DiffChoices struct {
	TestCases []Option
	Charts    []Option
}

// DiffSelection is what to render: empty TestCases or Charts mean the diff's defaults.
type DiffSelection struct {
	Base      string
	TestCases []string
	Charts    []string
}

// RunDiffViewer asks for a base if there is none, runs the diff and lets the user browse it full-screen.
// It returns the report, or nil when the user quit before it was ready.
func RunDiffViewer(ctx context.Context, s DiffSession) (*gitopsdiff.Report, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newDiffModel(s, cancel)
	p := tea.NewProgram(m)
	m.send = p.Send
	m.ctx = ctx
	_, err := p.Run()
	// Quitting while the diff runs stops it; wait, so its temporary worktree is removed before the CLI exits.
	cancel()
	if m.runDone != nil {
		<-m.runDone
	}
	if err != nil {
		return nil, err
	}
	return m.report, m.err
}

type diffPhase int

const (
	phaseBase diffPhase = iota
	phaseListing
	phaseSelect
	phaseRendering
	phaseBrowsing
	phaseFailed
)

type (
	progressMsg gitopsdiff.Progress
	choicesMsg  struct {
		choices DiffChoices
		err     error
	}
	reportMsg struct {
		report   *gitopsdiff.Report
		warnings string
		err      error
	}
)

type nodeKind int

const (
	nodeChart nodeKind = iota
	nodeTestCase
	nodeFile
	nodeFailure
	nodeValues
	nodeDefaults
	nodeWarnings
)

// diffNode is one line of the change tree; charts hold test cases, test cases hold files.
type diffNode struct {
	kind     nodeKind
	label    string
	path     string // chart, test case and file joined, for filtering
	stats    string
	file     *gitopsdiff.FileDiff
	failure  *gitopsdiff.Failure
	children []*diffNode
	expanded bool
	depth    int
}

type diffModel struct {
	session DiffSession
	base    string // the base chosen, or given
	cancel  context.CancelFunc
	ctx     context.Context
	send    func(tea.Msg)
	runDone chan struct{}
	start   time.Time

	width, height int
	phase         diffPhase
	spin          spinner.Model
	progress      gitopsdiff.Progress

	form       *huh.Form
	choice     string
	customBase string

	choices   *DiffChoices
	testCases []string // selected
	charts    []string // selected

	report   *gitopsdiff.Report
	warnings string
	err      error

	roots     []*diffNode
	cursor    int
	offset    int
	focusDiff bool
	filtering bool
	filter    string
	vp        viewport.Model
	shown     *diffNode
}

const otherBase = "other…"

func newDiffModel(s DiffSession, cancel context.CancelFunc) *diffModel {
	m := &diffModel{
		session: s,
		base:    s.Base,
		cancel:  cancel,
		ctx:     context.Background(),
		send:    func(tea.Msg) {},
		start:   time.Now(),
		spin:    spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(okStyle)),
		vp:      viewport.New(),
	}
	if s.Base == "" {
		m.phase = phaseBase
		m.form = m.baseForm()
	} else {
		m.phase = phaseListing // Init lists the choices, or starts rendering when there are none
	}
	return m
}

// selectForm asks which test cases and charts to render, one page each, starting from the current selection.
func (m *diffModel) selectForm() *huh.Form {
	options := func(opts []Option, selected []string) []huh.Option[string] {
		out := make([]huh.Option[string], len(opts))
		for i, o := range opts {
			out[i] = huh.NewOption(o.Label, o.Value).Selected(slices.Contains(selected, o.Value))
		}
		return out
	}
	atLeastOne := func(what string) func([]string) error {
		return func(v []string) error {
			if len(v) == 0 {
				return fmt.Errorf("select at least one %s", what)
			}
			return nil
		}
	}
	return huh.NewForm(
		huh.NewGroup(huh.NewMultiSelect[string]().Title("Render with which test cases?").
			Description("Each target type renders every chart with the values Argo CD would give it.").
			Options(options(m.choices.TestCases, m.testCases)...).Height(max(m.bodyHeight()-6, 5)).Validate(atLeastOne("test case")).Value(&m.testCases)),
		huh.NewGroup(huh.NewMultiSelect[string]().Title("Render which charts?").
			Description("The charts the change touches are selected.").Filterable(true).
			Options(options(m.choices.Charts, m.charts)...).Height(max(m.bodyHeight()-6, 5)).Validate(atLeastOne("chart")).Value(&m.charts)),
	).WithShowHelp(true).WithWidth(m.mainWidth())
}

// startListing finds what can be rendered against the base; the choices come back as a message.
func (m *diffModel) startListing() tea.Cmd {
	m.phase = phaseListing
	base, ctx, choices := m.base, m.ctx, m.session.Choices
	return func() tea.Msg {
		c, err := choices(ctx, base)
		return choicesMsg{choices: c, err: err}
	}
}

// afterBase continues once the base is known: ask for the selection when there are choices, else render.
func (m *diffModel) afterBase() tea.Cmd {
	if m.session.Choices != nil {
		return m.startListing()
	}
	return m.startRun()
}

// showSelect opens the selection form.
func (m *diffModel) showSelect() tea.Cmd {
	m.phase = phaseSelect
	m.form = m.selectForm()
	return m.form.Init()
}

// baseForm asks for the base: one of the candidates, or any revision typed in.
func (m *diffModel) baseForm() *huh.Form {
	options := huh.NewOptions(append(append([]string{}, m.session.Bases...), otherBase)...)
	if len(m.session.Bases) > 0 {
		m.choice = m.session.Bases[0]
	}
	return huh.NewForm(
		huh.NewGroup(huh.NewSelect[string]().Title("Compare against which base?").
			Description("The change is "+m.session.Head+".").Options(options...).Height(12).Value(&m.choice)),
		huh.NewGroup(huh.NewInput().Title("Base revision").Description("A branch, tag or commit.").Value(&m.customBase).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return fmt.Errorf("enter a revision")
				}
				return nil
			})).WithHideFunc(func() bool { return m.choice != otherBase }),
	).WithShowHelp(true)
}

func (m *diffModel) Init() tea.Cmd {
	if m.phase == phaseBase {
		return tea.Batch(m.spin.Tick, m.form.Init())
	}
	return tea.Batch(m.spin.Tick, m.afterBase())
}

// startRun runs the diff in the background; its progress and result come back as messages.
func (m *diffModel) startRun() tea.Cmd {
	m.phase, m.progress = phaseRendering, gitopsdiff.Progress{}
	done := make(chan struct{})
	m.runDone = done
	sel := DiffSelection{Base: m.base, TestCases: slices.Clone(m.testCases), Charts: slices.Clone(m.charts)}
	ctx, send, s := m.ctx, m.send, m.session
	return func() tea.Msg {
		defer close(done)
		report, err := s.Run(ctx, sel, func(p gitopsdiff.Progress) { send(progressMsg(p)) })
		warnings := ""
		if s.Warnings != nil {
			warnings = s.Warnings()
		}
		return reportMsg{report: report, warnings: warnings, err: err}
	}
}

func (m *diffModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.SetWidth(m.mainWidth())
		m.vp.SetHeight(m.bodyHeight())
		if m.form != nil {
			m.form = m.form.WithWidth(m.mainWidth())
		}
		m.showSelected(true)
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case progressMsg:
		m.progress = gitopsdiff.Progress(msg)
		return m, nil
	case choicesMsg:
		if msg.err != nil {
			m.phase, m.err = phaseFailed, msg.err
			return m, nil
		}
		m.choices = &msg.choices
		m.testCases, m.charts = selected(msg.choices.TestCases), selected(msg.choices.Charts)
		return m, m.showSelect()
	case reportMsg:
		if msg.err != nil {
			m.phase, m.err = phaseFailed, msg.err
			return m, nil
		}
		m.phase, m.report, m.warnings = phaseBrowsing, msg.report, msg.warnings
		m.roots = buildTree(msg.report, msg.warnings)
		m.cursor = 0
		m.showSelected(true)
		return m, nil
	case tea.KeyPressMsg:
		if m.form == nil {
			return m.key(msg)
		}
		switch {
		case msg.String() == "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case msg.String() == "esc" && m.phase == phaseSelect && m.report != nil:
			// Reselecting from a result: esc goes back to it unchanged.
			m.form, m.phase = nil, phaseBrowsing
			return m, nil
		}
	}
	if m.form == nil {
		return m, nil
	}
	fm, cmd := m.form.Update(msg)
	m.form = fm.(*huh.Form)
	switch m.form.State {
	case huh.StateCompleted:
		m.form = nil
		if m.phase == phaseSelect {
			return m, tea.Batch(cmd, m.startRun())
		}
		m.base = m.choice
		if m.choice == otherBase {
			m.base = strings.TrimSpace(m.customBase)
		}
		return m, tea.Batch(cmd, m.afterBase())
	case huh.StateAborted:
		m.cancel()
		return m, tea.Quit
	}
	return m, cmd
}

func (m *diffModel) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if m.filtering {
		switch k {
		case "enter":
			m.filtering = false
		case "esc":
			m.filtering, m.filter = false, ""
		case "backspace":
			if m.filter != "" {
				m.filter = m.filter[:len(m.filter)-1]
			}
		default:
			if msg.Text != "" {
				m.filter += msg.Text
			}
		}
		m.cursor = 0
		m.showSelected(true)
		return m, nil
	}
	switch k {
	case "ctrl+c", "q":
		m.cancel()
		return m, tea.Quit
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.showSelected(true)
			return m, nil
		}
		if m.focusDiff {
			m.focusDiff = false
			return m, nil
		}
		m.cancel()
		return m, tea.Quit
	}
	if m.phase != phaseBrowsing {
		return m, nil
	}
	switch k {
	case "r":
		if m.choices != nil {
			return m, m.showSelect()
		}
		return m, nil
	case "tab":
		m.focusDiff = !m.focusDiff
		return m, nil
	case "/":
		m.filtering, m.focusDiff = true, false
		return m, nil
	case "n":
		m.jumpFile(1)
		return m, nil
	case "p", "N":
		m.jumpFile(-1)
		return m, nil
	}
	if m.focusDiff {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	visible := m.visible()
	switch k {
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-m.bodyHeight())
	case "pgdown":
		m.move(m.bodyHeight())
	case "home", "g":
		m.move(-len(visible))
	case "end", "G":
		m.move(len(visible))
	case "enter", "space", "right", "l":
		if len(visible) == 0 {
			break
		}
		switch n := visible[m.cursor]; {
		case len(n.children) == 0:
			m.focusDiff = true // a leaf: read its diff
		case k == "enter" || k == "space":
			n.expanded = !n.expanded
		default:
			n.expanded = true
		}
	case "left", "h":
		if len(visible) == 0 {
			break
		}
		n := visible[m.cursor]
		if len(n.children) > 0 && n.expanded {
			n.expanded = false
		} else if parent := m.parentIndex(visible, m.cursor); parent >= 0 {
			m.cursor = parent
			m.showSelected(true)
		}
	}
	return m, nil
}

func (m *diffModel) move(delta int) {
	n := len(m.visible())
	if n == 0 {
		return
	}
	m.cursor = min(max(m.cursor+delta, 0), n-1)
	m.showSelected(false)
}

// jumpFile selects the next (dir 1) or previous (dir -1) file, opening the chart and test case it is in.
func (m *diffModel) jumpFile(dir int) {
	var files []*diffNode
	var walk func([]*diffNode)
	walk = func(ns []*diffNode) {
		for _, n := range ns {
			if n.kind == nodeFile && m.matches(n) {
				files = append(files, n)
			}
			walk(n.children)
		}
	}
	walk(m.roots)
	if len(files) == 0 {
		return
	}
	i := -1
	for j, f := range files {
		if f == m.shown {
			i = j
		}
	}
	next := files[((i+dir)%len(files)+len(files))%len(files)]
	if i < 0 && dir < 0 {
		next = files[len(files)-1]
	}
	for _, chart := range m.roots {
		for _, tc := range chart.children {
			for _, f := range tc.children {
				if f == next {
					chart.expanded, tc.expanded = true, true
				}
			}
		}
	}
	for j, n := range m.visible() {
		if n == next {
			m.cursor = j
		}
	}
	m.showSelected(true)
}

func (m *diffModel) parentIndex(visible []*diffNode, i int) int {
	for j := i - 1; j >= 0; j-- {
		if visible[j].depth < visible[i].depth {
			return j
		}
	}
	return -1
}

func (m *diffModel) matches(n *diffNode) bool {
	return m.filter == "" || strings.Contains(strings.ToLower(n.path), strings.ToLower(m.filter))
}

// visible are the tree lines shown: expanded children, or every node matching the filter with its parents.
func (m *diffModel) visible() []*diffNode {
	var out []*diffNode
	var walk func(ns []*diffNode) bool
	walk = func(ns []*diffNode) bool {
		any := false
		for _, n := range ns {
			mark := len(out)
			out = append(out, n)
			childMatch := false
			if n.expanded || m.filter != "" {
				childMatch = walk(n.children)
			}
			if m.filter != "" && !childMatch && !m.matches(n) {
				out = out[:mark]
				continue
			}
			any = true
		}
		return any
	}
	walk(m.roots)
	return out
}

// showSelected puts the selected node's content into the diff pane; reset scrolls it to the top.
func (m *diffModel) showSelected(reset bool) {
	visible := m.visible()
	if m.cursor >= len(visible) {
		m.cursor = max(len(visible)-1, 0)
	}
	var n *diffNode
	if len(visible) > 0 {
		n = visible[m.cursor]
	}
	if n == m.shown && !reset {
		return
	}
	m.shown = n
	m.vp.SetContent(m.content(n))
	m.vp.GotoTop()
}

func (m *diffModel) content(n *diffNode) string {
	if m.report == nil {
		return ""
	}
	if n == nil {
		if m.report.Empty() {
			return okStyle.Render("No changes in the rendered manifests, the default values or the effective values.")
		}
		return dimStyle.Render("Nothing matches the filter.")
	}
	switch n.kind {
	case nodeFile:
		return colorDiff(n.file.Text)
	case nodeTestCase, nodeChart:
		var b strings.Builder
		var walk func(*diffNode)
		walk = func(n *diffNode) {
			if n.file != nil {
				b.WriteString(n.file.Text)
			}
			for _, c := range n.children {
				walk(c)
			}
		}
		walk(n)
		return colorDiff(b.String())
	case nodeFailure:
		style := warnStyle
		switch n.failure.Status {
		case gitopsdiff.Broken:
			style = errStyle
		case gitopsdiff.Fixed:
			style = okStyle
		}
		return style.Render(gitopsdiff.FailureSubject(*n.failure)+": "+gitopsdiff.FailureLabel(n.failure.Status)) + "\n\n" + n.failure.Error
	case nodeValues:
		return valuesTable(m.report.ValueChanges)
	case nodeDefaults:
		return colorDiff(m.report.DefaultValuesDiff)
	case nodeWarnings:
		return m.warnings
	}
	return ""
}

func valuesTable(changes []gitopsdiff.ValueChange) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CHART\tTEST CASE\tPATH\tBEFORE\tAFTER\tSET BY")
	for _, c := range changes {
		before, after, src := output.Scalar(c.Before), output.Scalar(c.After), fmt.Sprintf("%s:%d", c.Source.File, c.Source.Line)
		switch c.Kind {
		case gitopsdiff.Added:
			before = "-"
		case gitopsdiff.Removed:
			after, src = "-", "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Chart, c.TestCase, c.Path, before, after, src)
	}
	_ = tw.Flush()
	return b.String()
}

var (
	addStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	delStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	hunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	cursorOn  = lipgloss.NewStyle().Reverse(true)
	cursorOff = lipgloss.NewStyle().Bold(true).Underline(true)
)

// colorDiff colors a unified diff: added lines green, removed red, hunk headers blue, file headers bold.
func colorDiff(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "diff "), strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "+++ "):
			lines[i] = headingStyle.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = hunkStyle.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = addStyle.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = delStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// buildTree arranges a report as charts → test cases → files, followed by failures, changed values, changed
// default values and helm's warnings.
func buildTree(r *gitopsdiff.Report, warnings string) []*diffNode {
	var roots []*diffNode
	byChart := map[string]*diffNode{}
	byTestCase := map[string]*diffNode{}
	for i := range r.Files {
		f := &r.Files[i]
		chart := byChart[f.Chart]
		if chart == nil {
			chart = &diffNode{kind: nodeChart, label: f.Chart, path: f.Chart}
			byChart[f.Chart] = chart
			roots = append(roots, chart)
		}
		tc := byTestCase[f.Chart+"/"+f.TestCase]
		if tc == nil {
			tc = &diffNode{kind: nodeTestCase, label: f.TestCase, path: f.Chart + "/" + f.TestCase, depth: 1}
			byTestCase[f.Chart+"/"+f.TestCase] = tc
			chart.children = append(chart.children, tc)
		}
		tc.children = append(tc.children, &diffNode{kind: nodeFile, label: f.Path, path: f.Chart + "/" + f.TestCase + "/" + f.Path, file: f, depth: 2})
	}
	for _, chart := range roots {
		var all []gitopsdiff.FileDiff
		for _, tc := range chart.children {
			var files []gitopsdiff.FileDiff
			for _, f := range tc.children {
				files = append(files, *f.file)
			}
			tc.stats = lines(files)
			all = append(all, files...)
		}
		chart.stats = lines(all)
	}
	for i := range r.Failures {
		f := &r.Failures[i]
		roots = append(roots, &diffNode{kind: nodeFailure, label: gitopsdiff.FailureSubject(*f), path: gitopsdiff.FailureSubject(*f), failure: f})
	}
	if len(r.ValueChanges) > 0 {
		roots = append(roots, &diffNode{kind: nodeValues, label: fmt.Sprintf("Changed values (%d)", len(r.ValueChanges)), path: "changed values"})
	}
	if r.DefaultValuesDiff != "" {
		roots = append(roots, &diffNode{kind: nodeDefaults, label: "Default values", path: "default values"})
	}
	if warnings != "" {
		roots = append(roots, &diffNode{kind: nodeWarnings, label: fmt.Sprintf("Helm warnings (%d)", strings.Count(warnings, "\nwarning:")+1), path: "helm warnings"})
	}
	if len(roots) > 0 && len(roots[0].children) > 0 {
		roots[0].expanded = true
	}
	return roots
}

func lines(files []gitopsdiff.FileDiff) string {
	add, del := 0, 0
	for _, f := range files {
		add += f.Additions
		del += f.Deletions
	}
	return fmt.Sprintf("+%d/-%d", add, del)
}

func (m *diffModel) sidebarWidth() int { return min(max(m.width/3, 30), 56) }
func (m *diffModel) mainWidth() int    { return max(m.width-m.sidebarWidth()-4, 20) }
func (m *diffModel) bodyHeight() int   { return max(m.height-4, 5) }

func (m *diffModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *diffModel) render() string {
	if m.width == 0 {
		return "starting..."
	}
	base := m.base
	if base == "" {
		base = "?"
	}
	header := headingStyle.Render(" kubriX · diff "+base+" … "+m.session.Head) + dimStyle.Render("  "+time.Since(m.start).Round(time.Second).String())
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	focused := box.BorderForeground(lipgloss.Color("39"))

	var main string
	switch m.phase {
	case phaseBase, phaseSelect:
		main = m.form.View()
	case phaseListing:
		main = strings.TrimSpace(m.spin.View()) + " finding the target types and charts…"
	case phaseRendering:
		main = strings.TrimSpace(m.spin.View()) + " rendering the charts of both revisions…"
		if m.progress.Total > 0 {
			main += fmt.Sprintf("\n\n%d of %d rendered\n%s", m.progress.Done, m.progress.Total, dimStyle.Render("last: "+m.progress.Last))
		}
	case phaseFailed:
		main = errStyle.Render("✗ " + m.err.Error())
	default:
		main = m.vp.View()
	}
	mainStyle, treeStyle := box, focused
	if m.focusDiff {
		mainStyle, treeStyle = focused, box
	}
	if m.phase != phaseBrowsing {
		mainStyle, treeStyle = focused, box
	}
	// Sizes include the border: the boxes hold bodyHeight lines.
	sidebar := treeStyle.Width(m.sidebarWidth()).Height(m.bodyHeight() + 2).Render(m.treeView(m.sidebarWidth()-2, m.bodyHeight()))
	mainBox := mainStyle.Width(m.mainWidth() + 2).Height(m.bodyHeight() + 2).MaxHeight(m.bodyHeight() + 2).Render(main)
	return lipgloss.JoinVertical(lipgloss.Left, header, lipgloss.JoinHorizontal(lipgloss.Top, sidebar, mainBox), m.statusView())
}

func (m *diffModel) treeView(width, height int) string {
	title := headingStyle.Render("Changes")
	if m.filter != "" || m.filtering {
		title += dimStyle.Render("  /" + m.filter)
		if m.filtering {
			title += "▏"
		}
	}
	if m.phase != phaseBrowsing {
		return title
	}
	visible := m.visible()
	rows := height - 1
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	lines := []string{title}
	for i := m.offset; i < len(visible) && i < m.offset+rows; i++ {
		n := visible[i]
		line := strings.Repeat("  ", n.depth) + m.icon(n) + " " + n.label
		if n.stats != "" {
			line += dimStyle.Render("  " + n.stats)
		}
		line = ansi.Truncate(line, width, "…")
		if i == m.cursor {
			style := cursorOff
			if !m.focusDiff {
				style = cursorOn
			}
			line = style.Render(ansi.Strip(line))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *diffModel) icon(n *diffNode) string {
	switch n.kind {
	case nodeChart, nodeTestCase:
		if n.expanded || m.filter != "" {
			return "▾"
		}
		return "▸"
	case nodeFile:
		switch n.file.Status {
		case gitopsdiff.Added:
			return addStyle.Render("+")
		case gitopsdiff.Removed:
			return delStyle.Render("-")
		}
		return hunkStyle.Render("~")
	case nodeFailure:
		switch n.failure.Status {
		case gitopsdiff.Broken:
			return errStyle.Render("✗")
		case gitopsdiff.Fixed:
			return okStyle.Render("✓")
		}
		return warnStyle.Render("!")
	}
	return "●"
}

func (m *diffModel) statusView() string {
	switch {
	case m.phase == phaseBase:
		return dimStyle.Render(" choose the base · ctrl+c to quit")
	case m.phase == phaseSelect && m.report != nil:
		return dimStyle.Render(" space toggle · enter next · esc back to the result")
	case m.phase == phaseSelect:
		return dimStyle.Render(" space toggle · / filter charts · enter next · ctrl+c to quit")
	case m.phase == phaseListing:
		return dimStyle.Render(" ctrl+c to quit")
	case m.phase == phaseRendering:
		return dimStyle.Render(" ctrl+c to stop")
	case m.phase == phaseFailed:
		return dimStyle.Render(" q to exit")
	case m.filtering:
		return dimStyle.Render(" type to filter · enter keep · esc clear")
	case m.focusDiff:
		return dimStyle.Render(" ↑/↓ PgUp/PgDn scroll · n/p next/prev file · tab tree · q quit")
	default:
		return dimStyle.Render(" ↑/↓ select · enter open · ←/→ fold · n/p file · / filter · tab diff · r reselect · q quit")
	}
}

// selected are the values of the selected options.
func selected(opts []Option) []string {
	var out []string
	for _, o := range opts {
		if o.Selected {
			out = append(out, o.Value)
		}
	}
	return out
}
