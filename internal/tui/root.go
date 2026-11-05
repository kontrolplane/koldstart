package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// Init lists the stages and starts the refresh, with the generations newModel numbered them by,
// as Init cannot change the model.
func (m model) Init() tea.Cmd {
	return tea.Batch(m.stagesCmd(m.state.overview.gen), refreshTick(m.refreshGen), m.spinner.Tick)
}

// busy reports whether something runs that the spinner shows.
func (m model) busy() bool {
	return m.loading || m.state.plan.applying || m.state.function.invoking
}

// keepSpinning restarts the spinner when something began after it last stopped.
func (m model) keepSpinning() (model, tea.Cmd) {
	if !m.busy() || m.spinning {
		return m, nil
	}
	m.spinning = true
	return m, m.spinner.Tick
}

// loadError reports a failed load. Data already on screen stays there with the error in the
// footer, so a refresh failing every few seconds does not keep raising a dialog.
func (m model) loadError(what string, err error, onScreen bool) (model, tea.Cmd) {
	if onScreen {
		return m.setStatus(what+": "+err.Error(), styles.ToneDanger)
	}
	m.error = what + ": " + err.Error()
	return m, nil
}

// copy puts text on the clipboard of the terminal.
func (m model) copy(text string) (model, tea.Cmd) {
	if text == "" {
		return m, nil
	}
	m, status := m.setStatus("copied "+truncate(styles.Clean(text), 48), styles.ToneSuccess)
	return m, tea.Batch(tea.SetClipboard(text), status)
}

// refresh reads the page on screen again.
func (m model) refresh() (model, tea.Cmd) {
	switch m.page {
	case overviewPage:
		return m.loadStages()
	case stagePage:
		m, cmd := m.loadStage()
		if m.state.stage.tab == historyTab {
			var history tea.Cmd
			m, history = m.loadHistory()
			cmd = tea.Batch(cmd, history)
		}
		return m, cmd
	case functionPage:
		return m.refreshFunction()
	}
	return m, nil
}

func (m model) togglePause() (model, tea.Cmd) {
	m.paused = !m.paused
	if m.paused {
		return m.setStatus("refresh paused", styles.ToneWarning)
	}
	m, tick := m.scheduleRefresh()
	m, status := m.setStatus("refresh resumed", styles.ToneSuccess)
	return m, tea.Batch(tick, status)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := m.update(msg)
	m, spin := m.keepSpinning()
	return m, tea.Batch(cmd, spin)
}

func (m model) update(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		setLayout(msg.Width, msg.Height)
		return m.resize(), nil
	case spinner.TickMsg:
		if !m.busy() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		return m.keyPress(msg)
	case stagesLoadedMsg:
		return m.applyStages(msg)
	case stageLoadedMsg:
		return m.applyStage(msg)
	case historyLoadedMsg:
		return m.applyHistory(msg)
	case versionLoadedMsg:
		return m.applyVersion(msg)
	case logsLoadedMsg:
		return m.applyLogs(msg)
	case tailMsg:
		return m.applyTail(msg)
	case functionLiveMsg:
		return m.applyFunctionLive(msg)
	case metricsLoadedMsg:
		return m.applyMetrics(msg)
	case queuesLoadedMsg:
		return m.applyQueues(msg)
	case liveLoadedMsg:
		return m.applyLive(msg)
	case invokedMsg:
		return m.applyInvoked(msg)
	case planLoadedMsg:
		return m.applyPlanLoaded(msg)
	case progressMsg:
		return m.applyProgress(msg)
	case appliedMsg:
		return m.applied(msg)
	case unlockedMsg:
		return m.applyUnlocked(msg)
	case eventEditedMsg:
		if msg.err != nil {
			m.error = "the editor failed: " + msg.err.Error()
		}
		return m.readEventFiles(), nil
	case statusClearMsg:
		if msg.gen == m.statusGen {
			m.status = ""
		}
	case refreshTickMsg:
		if msg.gen != m.refreshGen || m.paused {
			return m, nil
		}
		m, tick := m.scheduleRefresh()
		m, load := m.refresh()
		return m, tea.Batch(tick, load)
	}
	return m, nil
}

func (m model) keyPress(msg tea.KeyPressMsg) (model, tea.Cmd) {
	if key.Matches(msg, m.keys.ForceQuit) {
		return m, tea.Quit
	}
	if m.tooSmall() {
		return m, nil
	}
	if m.error != "" {
		m.error = ""
		return m, nil
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}
	if !m.typing() {
		switch {
		case key.Matches(msg, m.keys.Help):
			m.showHelp = true
			return m, nil
		case m.refreshes() && key.Matches(msg, m.keys.Pause):
			return m.togglePause()
		}
	}
	if m.loading && !key.Matches(msg, m.keys.Back, m.keys.Quit) {
		return m, nil
	}
	switch m.page {
	case overviewPage:
		return m.overviewUpdate(msg)
	case stagePage:
		return m.stageUpdate(msg)
	case functionPage:
		return m.functionUpdate(msg)
	case resourcePage:
		return m.resourceUpdate(msg)
	case versionPage:
		return m.versionUpdate(msg)
	case planPage:
		if m.loading {
			return m.leavePlan()
		}
		return m.planUpdate(msg)
	case unlockPage:
		return m.unlockUpdate(msg)
	case removePage:
		return m.removeUpdate(msg)
	case runLocalPage:
		return m.runLocalUpdate(msg)
	case diffPage:
		return m.diffUpdate(msg)
	case invocationPage:
		return m.invocationUpdate(msg)
	}
	return m, nil
}

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "kdst ui"
	v.ForegroundColor = styles.P.Text
	if styles.Paint {
		v.BackgroundColor = styles.P.Base
	}
	return v
}

func (m model) render() string {
	if m.tooSmall() {
		notice := lipgloss.JoinVertical(lipgloss.Center,
			styles.Render(styles.B("terminal too small", styles.ToneWarning)),
			styles.Muted(fmt.Sprintf("%d×%d, needs %d×%d", m.width, m.height, minContentWidth+chromeWidth, minContentHeight+chromeHeight)),
			styles.Faint("ctrl+c to quit"),
		)
		return place(m.width, m.height, notice)
	}
	meta, foot := m.frameMeta()
	view := m.header() + "\n\n" + frame(styles.Render(m.breadcrumb()...), meta, foot, m.content()) + "\n" + m.footer()
	if m.width == 0 {
		return view
	}
	return place(m.width, m.height, view)
}

// content is what the frame holds: the page, with the help or an error over it.
func (m model) content() string {
	page := m.pageView()
	switch {
	case m.showHelp:
		return overlay(page, m.helpView())
	case m.error != "":
		return overlay(page, dialog("error", styles.ToneDanger, styles.Body(styles.CleanBlock(m.error)), "", styles.Faint("press any key to close")))
	}
	return page
}

// pageView renders the page, a confirmation over the page it was asked from.
func (m model) pageView() string {
	if m.loading {
		return place(contentWidth, contentHeight, styles.Accent(m.spinner.View())+" "+styles.Muted(truncate(styles.Clean(m.loadingMsg), contentWidth-4)))
	}
	if from, ok := m.dialogFrom(); ok {
		return overlay(m.view(from), m.view(m.page))
	}
	return m.view(m.page)
}

// dialogFrom is the page a confirmation was asked from, which stays in sight behind it.
func (m model) dialogFrom() (page, bool) {
	switch m.page {
	case unlockPage:
		return m.state.unlock.from, true
	case removePage:
		return planPage, true
	case runLocalPage:
		return functionPage, true
	}
	return 0, false
}

func (m model) view(p page) string {
	switch p {
	case overviewPage:
		return m.overviewView()
	case stagePage:
		return m.stageView()
	case functionPage:
		return m.functionView()
	case resourcePage:
		return m.resourceView()
	case versionPage:
		return m.versionView()
	case planPage:
		return m.planView()
	case unlockPage:
		return m.unlockView()
	case removePage:
		return m.removeView()
	case runLocalPage:
		return m.runLocalView()
	case diffPage:
		return m.diffView()
	case invocationPage:
		return m.invocationView()
	}
	return ""
}

// fact is a label and its value in the header.
type fact struct {
	label string
	value cell
}

// header shows who the interface works as, and where: the profile, account, region and state
// bucket, and the project here.
func (m model) header() string {
	id := m.ident
	project := text("none here", styles.ToneFaint)
	if m.project != "" {
		project = cell{styles.S(m.project, styles.ToneText), styles.S(" · "+m.projectStage, styles.ToneMuted)}
	}
	columns := [][2]fact{
		{{"profile", text(id.Profile, styles.ToneBody)}, {"account", text(id.Account, styles.ToneBody)}},
		{{"region", text(id.Region, styles.ToneBody)}, {"bucket", text(id.Bucket, styles.ToneMuted)}},
		{{"project", project}, {"as", text(id.Caller, styles.ToneMuted)}},
	}
	width := frameWidth - 1
	each := (width - 2*(len(columns)-1)) / len(columns)
	var rendered []string
	for i, c := range columns {
		if i > 0 {
			rendered = append(rendered, "  ")
		}
		var lines []string
		for _, f := range c {
			lines = append(lines, styles.Faint(fmt.Sprintf("%-8s", f.label))+renderCell(f.value, column{width: max(1, each-8)}, nil))
		}
		rendered = append(rendered, strings.Join(lines, "\n"))
	}
	return indent(lipgloss.JoinHorizontal(lipgloss.Top, rendered...))
}

// crumbStages is the first crumb, the overview of every stage.
const crumbStages = "stages"

// breadcrumb names where the page sits: stages › orders/dev › functions › api.
func (m model) breadcrumb() []styles.Span {
	ref := m.state.stage.ref.String()
	var trail []string
	switch m.page {
	case overviewPage:
		trail = []string{crumbStages}
	case stagePage:
		trail = []string{crumbStages, ref, stageTabs[m.state.stage.tab]}
	case functionPage, runLocalPage:
		trail = []string{crumbStages, ref, stageTabs[functionsTab], m.state.function.name}
	case resourcePage:
		trail = []string{crumbStages, ref, stageTabs[resourcesTab], m.state.resource.res.id}
	case versionPage:
		trail = []string{crumbStages, ref, stageTabs[historyTab], formatTime(m.state.version.version.LastModified)}
	case planPage, removePage:
		trail = []string{crumbStages, m.state.plan.ref.String(), m.planWhat()}
	case unlockPage:
		trail = []string{crumbStages, m.state.unlock.ref.String(), m.keys.Unlock.Help().Desc}
	case diffPage:
		trail = []string{crumbStages, ref, "diff", m.state.diff.title}
	case invocationPage:
		trail = []string{crumbStages, ref, m.state.function.name, functionTabs[invocationsTab], shortSHA(m.state.invocation.req.id)}
	}
	var spans []styles.Span
	for i, t := range trail {
		if i > 0 {
			spans = append(spans, styles.S(" › ", styles.ToneFaint))
		}
		if i == len(trail)-1 {
			spans = append(spans, styles.B(t, styles.ToneText))
		} else {
			spans = append(spans, styles.S(truncate(t, 32), styles.ToneMuted))
		}
	}
	return spans
}

// frameMeta is what is set into the frame's edges: the filter on top, and at the bottom the
// position in the table, when the page was read, and whether refreshing is paused.
func (m model) frameMeta() (string, string) {
	var filter string
	var t *dataTable
	switch m.page {
	case overviewPage:
		filter, t = m.state.overview.filter.text, &m.state.overview.table
	case stagePage:
		s := &m.state.stage
		filter, t = s.filter.text, &s.tables[s.tab]
	case functionPage:
		f := &m.state.function
		switch f.tab {
		case logsTab:
			filter = f.logs.filter.text
		case invocationsTab:
			t = &f.invocations
		}
	}
	var meta string
	if filter != "" {
		meta = styles.Render(styles.S("filter ", styles.ToneFaint), styles.S(filter, styles.ToneText))
	}
	var foot []string
	if f := m.state.function; m.page == functionPage && f.tab == logsTab {
		switch {
		case !f.logs.follow:
			foot = append(foot, styles.Faint("follow off"))
		case f.logs.tailing:
			foot = append(foot, styles.Render(styles.S("● ", styles.ToneSuccess), styles.S("live tail", styles.ToneMuted)))
		default:
			foot = append(foot, styles.Render(styles.S("● ", styles.ToneAccent), styles.S("following, read every "+refreshEvery.String(), styles.ToneMuted)))
		}
	}
	if t != nil && t.position() != "" {
		foot = append(foot, styles.Faint(t.position()))
	}
	if !m.refreshedAt.IsZero() && m.refreshes() {
		foot = append(foot, styles.Faint("read "+formatAgo(m.refreshedAt)))
	}
	if m.paused {
		foot = append(foot, styles.Render(styles.S("⏸ paused", styles.ToneWarning)))
	}
	return meta, strings.Join(foot, styles.Faint(" · "))
}

func (m model) footer() string {
	var status string
	if m.status != "" {
		mark, tone := "✓ ", styles.ToneSuccess
		switch m.statusTone {
		case styles.ToneDanger:
			mark, tone = "✗ ", styles.ToneDanger
		case styles.ToneWarning:
			mark, tone = "▲ ", styles.ToneWarning
		}
		status = styles.Render(styles.S(mark, tone), styles.S(m.status, tone))
		status = ansi.Truncate(status, frameWidth*2/3, "…")
	}
	width := frameWidth - 3 - ansi.StringWidth(status)
	var left string
	switch {
	case m.page == overviewPage && m.state.overview.filter.active:
		left = m.state.overview.filter.view(width, m.keys)
	case m.page == stagePage && m.state.stage.filter.active:
		left = m.state.stage.filter.view(width, m.keys)
	case m.page == functionPage && m.state.function.tab == logsTab && m.state.function.logs.filter.active:
		left = m.state.function.logs.filter.view(width, m.keys)
	default:
		left = fitHints(width, m.shortHelp()...)
	}
	return " " + spread(ansi.Truncate(left, width, "…"), status+" ", frameWidth-1)
}

// fitHints renders the hints that fit width, dropping them from the end but for the last two,
// help and back or quit, which stay.
func fitHints(width int, pairs ...[2]string) string {
	pairs = slices.Clone(pairs)
	for len(pairs) > 2 && ansi.StringWidth(hints(pairs...)) > width {
		pairs = slices.Delete(pairs, len(pairs)-3, len(pairs)-2)
	}
	return hints(pairs...)
}

func hints(pairs ...[2]string) string {
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = styles.Key(p[0], p[1])
	}
	return strings.Join(parts, styles.Faint("  ·  "))
}

// hint is a binding as the footer shows it, with what it does here when that is not its own help.
func hint(b key.Binding, desc ...string) [2]string {
	h := b.Help()
	if len(desc) > 0 {
		h.Desc = desc[0]
	}
	return [2]string{h.Key, h.Desc}
}

// scrollHint stands for the keys that scroll a page.
var (
	scrollHint = [2]string{upDown, "scroll"}
	chooseHint = [2]string{upDown, "choose"}
)

// upDown names the arrow keys that move up and down.
const upDown = "↑/↓"

func (m model) confirmHints() [][2]string {
	k := m.keys
	return [][2]string{{"y/n", "yes/no"}, {"←/→", chooseHint[1]}, hint(k.View, "confirm"), hint(k.Back, "cancel")}
}

// shortHelp is the hints the footer shows for the page.
func (m model) shortHelp() [][2]string {
	k := m.keys
	if m.error != "" || m.showHelp {
		return [][2]string{{"any key", "close"}}
	}
	switch m.page {
	case overviewPage:
		return [][2]string{hint(k.View), hint(k.Deploy), hint(k.Remove), hint(k.Unlock), hint(k.Filter), hint(k.Copy), hint(k.Pause), hint(k.Help), hint(k.Quit, "quit")}
	case stagePage:
		return [][2]string{hint(k.View), hint(k.NextTab), hint(k.Deploy), hint(k.Remove), hint(k.Unlock), hint(k.Filter), hint(k.Copy), hint(k.Help), hint(k.Quit)}
	case functionPage:
		var h [][2]string
		switch m.state.function.tab {
		case logsTab:
			h = append(h, hint(k.Follow), hint(k.Filter), [2]string{"↑ at top", "older"})
		case invocationsTab:
			h = append(h, hint(k.View, "open"))
		case invokeTab:
			h = append(h, hint(k.EditEvent))
		}
		h = append(h, hint(k.NextTab), hint(k.Invoke), hint(k.RunLocal), hint(k.Deploy, "plan its code"))
		return append(h, hint(k.Copy), hint(k.Help), hint(k.Quit))
	case resourcePage:
		return [][2]string{hint(k.NextTab, "applied / in aws"), scrollHint, hint(k.Refresh), hint(k.Copy), hint(k.Help), hint(k.Quit)}
	case versionPage:
		return [][2]string{hint(k.View, "diff"), chooseHint, hint(k.Copy), hint(k.Help), hint(k.Quit)}
	case diffPage, invocationPage:
		return [][2]string{scrollHint, hint(k.Help), hint(k.Quit)}
	case planPage:
		p := m.state.plan
		switch {
		case p.applying:
			return [][2]string{scrollHint, hint(k.ForceQuit, "stop and quit")}
		case p.done || p.plan == nil:
			return [][2]string{hint(k.View, "diff"), chooseHint, hint(k.Help), hint(k.Quit)}
		}
		if p.remove || p.function != "" || p.refresh {
			return [][2]string{hint(k.Apply, m.planWhat()), hint(k.View, "diff"), chooseHint, hint(k.Help), hint(k.Quit)}
		}
		return [][2]string{hint(k.Apply, m.planWhat()), hint(k.View, "diff"), hint(k.Refresh, "read aws"), chooseHint, hint(k.Help), hint(k.Quit)}
	case removePage:
		return [][2]string{hint(k.View, "remove"), hint(k.Back, "cancel")}
	case unlockPage, runLocalPage:
		return m.confirmHints()
	}
	return nil
}

// helpSections are the columns of the key reference.
var helpSections = []struct {
	title string
	rows  [][2]string
}{
	{"move", [][2]string{{"↑/k ↓/j", "up / down"}, {"g / G", "first / last"}, {"pgup/pgdn", "page"}, {keys.Keys.View.Help().Key, "open"}, {"tab/⇧tab", "next / previous tab"}, {"/", "filter"}, {"q", "back, quit on stages"}, {"esc", "back, clear, cancel"}, {"ctrl+c", "quit"}}},
	{"stage", [][2]string{{"d", "plan a deploy"}, {"ctrl+s", "apply the plan"}, {"r on a plan", "plan again from aws as it is"}, {keys.Keys.View.Help().Key, "a change's diff"}, {"ctrl+d", "remove the stage"}, {"ctrl+x", "release a lock"}, {"c", "copy"}, {"r / p", "refresh / pause"}}},
	{"function", [][2]string{{"i", "invoke in aws"}, {"shift+i", "run on this machine"}, {"e", "edit the event"}, {"d", "plan its code"}, {"f", "follow the logs"}, {"/", "filter the logs"}, {"↑ at top", "older logs"}, {keys.Keys.View.Help().Key, "an invocation"}}},
}

// helpView draws the key reference in a card, as roomy as the content area allows.
func (m model) helpView() string {
	keyStyle := styles.Fg(styles.ToneText).Bold(true).Width(11)
	columns := make([]string, 0, len(helpSections))
	for i, s := range helpSections {
		lines := []string{styles.Fg(styles.ToneAccent).Bold(true).MarginBottom(1).Render(s.title)}
		for _, r := range s.rows {
			lines = append(lines, keyStyle.Render(r[0])+styles.Muted(r[1]))
		}
		column := lipgloss.JoinVertical(lipgloss.Left, lines...)
		if i < len(helpSections)-1 {
			column = lipgloss.NewStyle().MarginRight(4).Render(column)
		}
		columns = append(columns, column)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, columns...)
	if lipgloss.Width(body) > contentWidth-8 {
		body = lipgloss.JoinVertical(lipgloss.Left, columns...)
	}
	body = lipgloss.JoinVertical(lipgloss.Center, body, "", styles.Faint("● marks the app of the project here, the one d plans and shift+i runs"),
		styles.Faint("events to invoke with are kept in .koldstart/events/<function>/"), "", styles.Faint("press any key to close"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(styles.P.RuleBold).Padding(1, 3).
		Render(clip(body, contentWidth-10, contentHeight-4))
}
