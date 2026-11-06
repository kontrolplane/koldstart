package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// Init lists the stages and starts the refresh, with the generations newModel numbered them by,
// as Init cannot change the model. With --theme auto it asks the terminal for its background,
// which follows as a tea.BackgroundColorMsg, or not at all.
func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.stagesCmd(m.state.overview.gen), refreshTick(m.refreshGen), m.spinner.Tick}
	if !styles.Paint {
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	return tea.Batch(cmds...)
}

// busy reports whether something runs that the spinner shows.
func (m model) busy() bool {
	r := m.state.resource
	reading := m.page == resourcePage && r.tab == liveTab && !r.liveLoaded && readable(r.res)
	return m.loading || m.state.plan.applying || m.state.function.invoking || m.state.function.logs.loadingOlder || reading
}

// keepSpinning restarts the spinner when something began after it last stopped.
func (m model) keepSpinning() (model, tea.Cmd) {
	if !m.busy() || m.spinning {
		return m, nil
	}
	m.spinning = true
	return m, m.spinner.Tick
}

// loadError reports a failed load. Data already on screen stays there, marked as of when it was
// read, with the error in the footer, so a refresh failing every few seconds does not keep
// raising a dialog.
func (m model) loadError(what string, err error, onScreen bool) (model, tea.Cmd) {
	if onScreen {
		m.stale = !m.refreshedAt.IsZero()
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

// refresh reads the page on screen again, on the refresh's tick. What is still being read since
// the last tick is not asked for again: a read slower than refreshEvery would otherwise be
// dropped as stale every time, and the page would never change.
func (m model) refresh() (model, tea.Cmd) {
	switch m.page {
	case overviewPage:
		if o := m.state.overview; o.gen != o.answered {
			return m, nil
		}
		return m.loadStages()
	case stagePage:
		var cmds []tea.Cmd
		var cmd tea.Cmd
		s := m.state.stage
		if s.gen == s.answered {
			m, cmd = m.loadStage()
			cmds = append(cmds, cmd)
		}
		if s.tab == historyTab && s.historyGen == s.historyAnswered {
			m, cmd = m.loadHistory()
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	case functionPage:
		return m.refreshFunction(true)
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
	case tea.BackgroundColorMsg:
		if !styles.Paint {
			styles.Use(msg.IsDark())
		}
		return m.restyleInputs(), nil
	case tea.ColorProfileMsg:
		styles.UseProfile(msg.Profile)
		return m.restyleInputs(), nil
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
	case summariesLoadedMsg:
		return m.applySummaries(msg)
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
		return m.applyEventEdited(msg)
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
	default:
		return m.toInput(msg)
	}
	return m, nil
}

// toInput hands a message the model does not know, as the blink of a cursor or a paste, to the
// text input that has focus.
func (m model) toInput(msg tea.Msg) (model, tea.Cmd) {
	f := &m.state.function
	v := m.state.json(m.page)
	switch {
	case m.tooSmall():
	case v != nil && v.search.active:
		res := v.typeSearch(msg, m.keys)
		switch m.page {
		case resourcePage:
			m = m.followResourceCursor(tea.KeyPressMsg{}, -1)
		case outputPage:
			m = m.followOutputCursor(tea.KeyPressMsg{}, -1)
		case functionPage:
			m = m.followPane(tea.KeyPressMsg{}, -1)
		case logLinePage:
			m = m.followLogLineCursor(tea.KeyPressMsg{}, -1)
		}
		return m.jsonDone(res)
	case m.page == overviewPage && m.state.overview.filter.active:
		changed, cmd := m.state.overview.filter.update(msg, m.keys)
		if changed {
			m = m.updateOverviewTable()
		}
		return m, cmd
	case m.page == stagePage && m.state.stage.filter.active:
		changed, cmd := m.state.stage.filter.update(msg, m.keys)
		if changed {
			m = m.updateStageTables()
		}
		return m, cmd
	case m.page == functionPage && f.tab == logsTab && f.logs.filter.active:
		changed, cmd := f.logs.filter.update(msg, m.keys)
		if changed {
			m = m.logsRelaid()
		}
		return m, cmd
	case m.page == removePage:
		return m.removeUpdate(msg)
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
			return m.cancelPlan()
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
	case outputPage:
		return m.outputUpdate(msg)
	case inputsPage:
		return m.inputsUpdate(msg)
	case logLinePage:
		return m.logLineUpdate(msg)
	}
	return m, nil
}

func (m model) View() tea.View {
	content := m.render()
	if styles.Paint {
		content = styles.Ground(content, m.width, m.height)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = binary + " ui"
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
	view := m.header() + "\n\n" + frame(m.breadcrumb(titleRoom(meta)), meta, foot, m.content()) + "\n" + m.footer()
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
	case outputPage:
		return m.outputView()
	case inputsPage:
		return m.inputsView()
	case logLinePage:
		return m.logLineView()
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
		{{"profile", text(orDash(id.Profile), styles.ToneBody)}, {"account", text(orDash(id.Account), styles.ToneBody)}},
		{{"region", text(orDash(id.Region), styles.ToneBody)}, {"bucket", text(orDash(id.Bucket), styles.ToneMuted)}},
		{{"project", project}, {"as", text(orDash(callerName(id.Caller)), styles.ToneMuted)}},
	}
	want := make([]int, len(columns))
	for i, c := range columns {
		for _, f := range c {
			want[i] = max(want[i], headerLabelWidth+cellWidth(f.value))
		}
	}
	widths := shareWidths(want, frameWidth-1-2*(len(columns)-1))
	var rendered []string
	for i, c := range columns {
		if i > 0 {
			rendered = append(rendered, "  ")
		}
		var lines []string
		for _, f := range c {
			value := renderCell(f.value, column{width: max(1, widths[i]-headerLabelWidth)}, nil)
			lines = append(lines, styles.Faint(fmt.Sprintf("%-*s", headerLabelWidth, f.label))+value)
		}
		rendered = append(rendered, strings.Join(lines, "\n"))
	}
	return indent(lipgloss.JoinHorizontal(lipgloss.Top, rendered...))
}

// headerLabelWidth is the room of a label in the header, the value beside it.
const headerLabelWidth = 8

// cellWidth is how wide a cell's text is.
func cellWidth(c cell) int {
	w := 0
	for _, s := range c {
		w += ansi.StringWidth(s.Text)
	}
	return w
}

// shareWidths shares width among columns that want so much each. When every column fits an
// equal share, each gets one, so the columns line up as they always do. Otherwise a column that
// wants less than its share keeps what it wants, and what it leaves goes to the others, so a
// long state bucket is not cut while the project beside it leaves room. What is left once every
// column has what it wants is shared out evenly.
func shareWidths(want []int, width int) []int {
	n := len(want)
	out := make([]int, n)
	if n == 0 {
		return out
	}
	if slices.Max(want) <= width/n {
		for i := range out {
			out[i] = width / n
		}
		return out
	}
	open := make([]bool, n)
	for i := range open {
		open[i] = true
	}
	left, count := width, n
	for settled := true; settled && count > 0; {
		settled = false
		for i, w := range want {
			if open[i] && w <= left/count {
				out[i], open[i] = w, false
				left -= w
				count--
				settled = true
			}
		}
	}
	for i := range out {
		switch {
		case count == 0:
			out[i] += left / n
		case open[i]:
			out[i] = left / count
		}
	}
	return out
}

// callerName is the arn of the caller without its partition, service and account, which the
// header shows already: user/levi, or assumed-role/admin/levi. What is not an arn stays whole.
func callerName(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" || parts[5] == "" {
		return arn
	}
	return parts[5]
}

// crumbStages is the first crumb, the overview of every stage.
const crumbStages = "stages"

// crumbSep goes between the crumbs, and crumbWidth is the widest a crumb before the last gets.
const (
	crumbSep   = " › "
	crumbWidth = 32
)

// trail names where a page sits, from the stages down: a diff and an invocation under the page
// they were opened from.
func (m model) trail(p page) []string {
	ref := m.state.stage.ref.String()
	switch p {
	case overviewPage:
		return []string{crumbStages}
	case stagePage:
		return []string{crumbStages, ref, stageTabs[m.state.stage.tab]}
	case functionPage, runLocalPage:
		f := m.state.function
		return []string{crumbStages, ref, stageTabs[functionsTab], f.name, functionTabs[f.tab]}
	case resourcePage:
		if m.state.resource.fromFunction {
			return append(m.trail(functionPage), m.state.resource.res.id)
		}
		return []string{crumbStages, ref, stageTabs[resourcesTab], m.state.resource.res.id}
	case versionPage:
		return []string{crumbStages, ref, stageTabs[historyTab], formatTime(m.state.version.version.LastModified)}
	case planPage, removePage:
		return []string{crumbStages, m.state.plan.ref.String(), m.planWhat()}
	case unlockPage:
		return []string{crumbStages, m.state.unlock.ref.String(), m.keys.Unlock.Help().Desc}
	case diffPage:
		from := m.state.diff.from
		if from == diffPage {
			from = overviewPage
		}
		return append(m.trail(from), m.state.diff.title)
	case invocationPage:
		return append(m.trail(functionPage), shortRequest(m.state.invocation.req.id))
	case outputPage:
		return []string{crumbStages, ref, stageTabs[outputsTab], m.state.output.name}
	case inputsPage:
		return append(m.trail(planPage), m.state.inputs.id)
	case logLinePage:
		return append(m.trail(functionPage), "line "+m.state.logLine.e.Time.Local().Format("15:04:05.000"))
	}
	return []string{crumbStages}
}

// breadcrumb names where the page sits in width columns: stages › orders/dev › functions › api ›
// logs, the page on screen in bold.
func (m model) breadcrumb(width int) string {
	trail := m.trail(m.page)
	for i := range trail {
		trail[i] = styles.Clean(trail[i])
		if i < len(trail)-1 {
			trail[i] = truncate(trail[i], crumbWidth)
		}
	}
	crumbs := foldCrumbs(trail, width)
	last := len(crumbs) - 1
	var spans []styles.Span
	for i, c := range crumbs {
		if i > 0 {
			spans = append(spans, styles.S(crumbSep, styles.ToneFaint))
		}
		switch {
		case i == last:
			spans = append(spans, styles.B(c, styles.ToneText))
		case c == "…" && i == 1:
			spans = append(spans, styles.S(c, styles.ToneFaint))
		default:
			spans = append(spans, styles.S(c, styles.ToneMuted))
		}
	}
	return styles.Render(spans...)
}

// foldCrumbs fits a trail to width by giving way in the middle: the crumbs after the first fold
// into an ellipsis from the left until the two last are left, then the one before the last is
// cut, and the last, the page on screen, only once nothing else gives way.
func foldCrumbs(trail []string, width int) []string {
	fits := func(crumbs []string) bool { return ansi.StringWidth(strings.Join(crumbs, crumbSep)) <= width }
	crumbs := slices.Clone(trail)
	for n := 1; !fits(crumbs) && n < len(trail)-2; n++ {
		crumbs = append([]string{trail[0], "…"}, trail[1+n:]...)
	}
	cut := func(i int) {
		others := ansi.StringWidth(strings.Join(crumbs, crumbSep)) - ansi.StringWidth(crumbs[i])
		crumbs[i] = truncate(crumbs[i], max(1, width-others))
	}
	if !fits(crumbs) && len(crumbs) >= 3 {
		cut(len(crumbs) - 2)
	}
	if !fits(crumbs) {
		cut(len(crumbs) - 1)
	}
	return crumbs
}

// titleRoom is the room the frame's top edge leaves its title beside meta.
func titleRoom(meta string) int {
	if meta == "" {
		return frameWidth - 7
	}
	return frameWidth - 9 - min(ansi.StringWidth(meta), frameWidth/3)
}

// position is where the page on screen is, for the frame's foot: the row under the cursor of a
// table or a list of changes, as 12 of 240, or the lines of a page that scrolls in sight, as
// lines 12–40 of 230.
func (m model) position() string {
	if m.loading {
		return ""
	}
	switch m.page {
	case overviewPage:
		return m.state.overview.table.position()
	case stagePage:
		s := m.state.stage
		return s.tables[s.tab].position()
	case functionPage:
		return m.functionPosition()
	case resourcePage:
		return scrollCue(m.state.resource.scroll, contentHeight, m.resourceLines().len())
	case versionPage:
		v := m.state.version
		l := m.versionLines()
		if len(l.rows) == 0 {
			return scrollCue(v.scroll, contentHeight, l.lines.len())
		}
		return l.position(v.cursor)
	case planPage:
		return m.planPosition()
	case diffPage:
		return m.diffPosition()
	case invocationPage:
		return scrollCue(m.state.invocation.scroll, contentHeight, m.invocationLines().len())
	case outputPage:
		return scrollCue(m.state.output.scroll, contentHeight, m.outputLines().len())
	case inputsPage:
		if v := m.state.inputs.shown(); v != nil {
			return ofCount(v.at(), len(v.rows()))
		}
	case logLinePage:
		return scrollCue(m.state.logLine.scroll, contentHeight, m.logLineLines().len())
	}
	return ""
}

// json is the json view page p shows, where it shows one with something in it, so the frame can
// show its path and search, and its search can take what is typed. A page with a jsonView
// returns it here.
func (s *pages) json(p page) *jsonView {
	switch p {
	case resourcePage:
		return s.resource.shown()
	case outputPage:
		return s.output.shown()
	case functionPage:
		if f := &s.function; f.tab == invokeTab && f.pane.focused {
			return &f.pane.view
		}
	case inputsPage:
		return s.inputs.shown()
	case logLinePage:
		return s.logLine.shown()
	}
	return nil
}

// pageFilter is the filter of the page on screen, if it has one: the search of its json too.
func (m model) pageFilter() *filterBox {
	if v := m.state.json(m.page); v != nil {
		return &v.search
	}
	switch m.page {
	case overviewPage:
		return &m.state.overview.filter
	case stagePage:
		return &m.state.stage.filter
	case functionPage:
		if m.state.function.tab == logsTab {
			return &m.state.function.logs.filter
		}
	}
	return nil
}

// frameMeta is what is set into the frame's edges: the filter on top, while it is typed or
// holds text, and at the bottom where the page is, when it was read, and whether refreshing is
// paused.
func (m model) frameMeta() (string, string) {
	var meta string
	if f := m.pageFilter(); f != nil && (f.active || f.text != "") {
		meta = styles.Render(styles.S(f.name(), styles.ToneFaint))
		if f.text != "" {
			meta = styles.Render(styles.S(f.name()+" ", styles.ToneFaint), styles.S(f.text, styles.ToneText))
		}
	}
	if level := m.logsMeta(); level != "" && m.page == functionPage && m.state.function.tab == logsTab {
		if meta != "" {
			meta += styles.Faint(" · ")
		}
		meta += level
	}
	var foot []footPart
	if v := m.state.json(m.page); v != nil {
		if at := v.matchPosition(); at != "" {
			meta += styles.Render(styles.S(" · "+at, styles.ToneFaint))
		}
		if p := styles.Clean(v.path()); p != "" {
			foot = append(foot, footPart{full: styles.Faint(cutMiddle(p, frameWidth/2)), short: styles.Faint(cutMiddle(p, frameWidth/4))})
		}
	}
	if f := m.state.function; m.page == functionPage && f.tab == logsTab {
		switch {
		case !f.logs.follow:
			foot = append(foot, footPart{full: styles.Faint("follow off")})
		case f.logs.tailing:
			foot = append(foot, footPart{full: styles.Render(styles.S("● ", styles.ToneSuccess), styles.S("live tail", styles.ToneMuted))})
		default:
			foot = append(foot, footPart{
				full:  styles.Render(styles.S("● ", styles.ToneAccent), styles.S("following, read every "+refreshEvery.String(), styles.ToneMuted)),
				short: styles.Render(styles.S("● ", styles.ToneAccent), styles.S("following", styles.ToneMuted)),
			})
		}
	}
	if p := m.position(); p != "" {
		foot = append(foot, footPart{full: styles.Faint(p), short: styles.Faint(strings.TrimPrefix(p, "lines "))})
	}
	switch at := m.refreshedAt.Local().Format(time.TimeOnly); {
	case m.refreshedAt.IsZero() || !m.refreshes():
	case m.stale:
		foot = append(foot, footPart{
			full:  styles.Render(styles.S("as of "+at+", could not read again", styles.ToneWarning)),
			short: styles.Render(styles.S("as of "+at, styles.ToneWarning)),
		})
	default:
		foot = append(foot, footPart{full: styles.Faint("read at " + at)})
	}
	if m.paused {
		foot = append(foot, footPart{full: styles.Render(styles.S("⏸ paused", styles.ToneWarning))})
	}
	return meta, fitFoot(foot, frameWidth-6)
}

// footPart is a part of the frame's foot, with a shorter form for when the foot does not fit.
type footPart struct {
	full, short string
}

// fitFoot joins the parts of the foot in width: while they do not fit, they take their shorter
// form from the left, so the follow state and the position give way before a stale read does, and
// the pause is never cut short.
func fitFoot(parts []footPart, width int) string {
	join := func() string {
		texts := make([]string, len(parts))
		for i, p := range parts {
			texts[i] = p.full
		}
		return strings.Join(texts, styles.Faint(" · "))
	}
	for i := range parts {
		if ansi.StringWidth(join()) <= width {
			break
		}
		if parts[i].short != "" {
			parts[i].full = parts[i].short
		}
	}
	return join()
}

func (m model) footer() string {
	var status string
	if m.status != "" {
		mark, tone := "✓ ", styles.ToneSuccess
		text, budget := m.status, frameWidth*2/3
		switch m.statusTone {
		case styles.ToneDanger:
			mark, tone = "✗ ", styles.ToneDanger
			text = cutError(text, budget-ansi.StringWidth(mark))
		case styles.ToneWarning:
			mark, tone = "▲ ", styles.ToneWarning
		}
		status = styles.Render(styles.S(mark, tone), styles.S(text, tone))
		status = ansi.Truncate(status, budget, "…")
	}
	width := frameWidth - 3 - ansi.StringWidth(status)
	left := fitHints(width, m.shortHelp()...)
	if f := m.pageFilter(); f != nil && f.active {
		left = f.view(width, m.keys)
	}
	return " " + spread(ansi.Truncate(left, width, "…"), status+" ", frameWidth-1)
}

// cutError fits an error in width by cutting out its middle: what failed, up to the first colon,
// stays, and so does the end, where an error from aws says why, as connection refused or the
// permission it lacks. When the head would leave the end too little room, the end alone stays.
func cutError(s string, width int) string {
	if ansi.StringWidth(s) <= width || width < 8 {
		return s
	}
	head, _, ok := strings.Cut(s, ": ")
	if head += ": "; !ok || width-ansi.StringWidth(head)-1 < minErrorTail {
		head = ""
	}
	room := width - ansi.StringWidth(head) - 1
	runes := []rune(s)
	start := len(runes)
	for start > 0 && ansi.StringWidth(string(runes[start-1:])) <= room {
		start--
	}
	return head + "…" + string(runes[start:])
}

// minErrorTail is the least of the end of an error cutError keeps beside its head.
const minErrorTail = 24

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
	if m.loading && m.page != planPage {
		if m.page == overviewPage {
			return [][2]string{hint(k.Help), hint(k.Quit, "quit")}
		}
		return [][2]string{hint(k.Help), hint(k.Quit)}
	}
	switch m.page {
	case overviewPage:
		return m.overviewHints()
	case stagePage:
		return m.stageHints()
	case functionPage:
		var h [][2]string
		switch m.state.function.tab {
		case overviewTab:
			h = append(h, m.functionOverviewHints()...)
		case logsTab:
			h = append(h, hint(k.View, "open line"), hint(k.Follow), hint(k.Filter), hint(k.Level), hint(k.Group), hint(k.Stamps), [2]string{"↑ at top", "older"})
		case invocationsTab:
			h = append(h, hint(k.View, "open"))
		case invokeTab:
			return m.invokeKeys()
		}
		h = append(h, hint(k.NextTab), hint(k.Invoke))
		if m.b.Local(m.state.stage.ref) {
			h = append(h, hint(k.RunLocal), hint(k.Deploy, "plan its code"))
		}
		return append(h, hint(k.Copy), m.pauseHint(), hint(k.Help), hint(k.Quit))
	case resourcePage:
		h := [][2]string{hint(k.NextTab, "applied / in aws")}
		if v := m.state.json(m.page); v != nil {
			h = append(h, m.jsonHints(*v)...)
		} else {
			h = append(h, scrollHint, hint(k.Copy))
		}
		return append(h, hint(k.Refresh), hint(k.Help), hint(k.Quit))
	case versionPage:
		return [][2]string{hint(k.View, "diff"), chooseHint, hint(k.Copy), hint(k.Help), hint(k.Quit)}
	case diffPage:
		return m.diffHints()
	case inputsPage:
		return m.inputsHints()
	case invocationPage:
		return [][2]string{scrollHint, {k.Next.Help().Key + "/" + k.Previous.Help().Key, "next/previous"}, hint(k.Copy, "copy id"), hint(k.Help), hint(k.Quit)}
	case logLinePage:
		return m.logLineHints()
	case outputPage:
		if v := m.state.json(m.page); v != nil {
			return append(m.jsonHints(*v), hint(k.Refresh), hint(k.Help), hint(k.Quit))
		}
		return [][2]string{scrollHint, hint(k.Copy, "copy the value"), hint(k.Refresh), hint(k.Help), hint(k.Quit)}
	case planPage:
		return m.planHints()
	case removePage:
		return [][2]string{hint(k.View, "remove"), hint(k.Back, "cancel")}
	case unlockPage, runLocalPage:
		return m.confirmHints()
	}
	return nil
}

// overviewHints are the keys that do something to the stage under the cursor: d only on the
// project's app, ctrl+x only on a stage that is locked, ctrl+d only on one deployed.
func (m model) overviewHints() [][2]string {
	k := m.keys
	quit := hint(k.Quit, "quit")
	s, ok := m.selectedStage()
	if !ok {
		return [][2]string{hint(k.Filter), hint(k.Refresh), m.pauseHint(), hint(k.Help), quit}
	}
	h := [][2]string{hint(k.View)}
	if m.b.Local(s.StageRef) {
		h = append(h, hint(k.Deploy))
	}
	if s.Lock != nil {
		h = append(h, hint(k.Unlock))
	}
	h = append(h, hint(k.Filter))
	if !notDeployed(s) {
		h = append(h, hint(k.Remove))
	}
	return append(h, hint(k.Copy), m.pauseHint(), hint(k.Help), quit)
}

// stageHints are the keys of the stage's tab: enter where a row opens, or folds on the heading
// of a kind of resource, space where the resources fold, d only on the project's app, and ctrl+x
// only while the stage is locked.
func (m model) stageHints() [][2]string {
	k := m.keys
	s := m.state.stage
	rows := len(s.tables[s.tab].rows) > 0
	var h [][2]string
	if row, ok := s.selectedRow(); ok && s.tab == resourcesTab && row.res < 0 {
		h = append(h, hint(k.View, "fold"))
	} else if rows {
		h = append(h, hint(k.View))
	}
	if rows && s.tab == resourcesTab && s.filter.text == "" {
		h = append(h, hint(k.Fold))
	}
	h = append(h, hint(k.NextTab))
	if m.b.Local(s.ref) {
		h = append(h, hint(k.Deploy))
	}
	if s.lock != nil {
		h = append(h, hint(k.Unlock))
	}
	h = append(h, hint(k.Filter), hint(k.Remove))
	if rows {
		h = append(h, hint(k.Copy))
	}
	return append(h, m.pauseHint(), hint(k.Help), hint(k.Quit))
}

// pauseHint is p as it works now: pause the refresh, or resume it.
func (m model) pauseHint() [2]string {
	if m.paused {
		return hint(m.keys.Pause, "resume")
	}
	return hint(m.keys.Pause)
}

// helpSection is a column of the key reference: a title, and each key with what it does.
type helpSection struct {
	title string
	rows  [][2]string
}

// helpSections are the columns of the key reference, the keys named as the key map names them.
func helpSections(k keys.KeyMap) []helpSection {
	name := func(b key.Binding) string { return b.Help().Key }
	return []helpSection{
		{"everywhere", [][2]string{
			{name(k.Up) + " " + name(k.Down), "up / down"}, {name(k.Top) + " " + name(k.Bottom), "first / last"},
			{name(k.PageUp) + " " + name(k.PageDown), "page up / down"}, {name(k.View), "open"},
			{name(k.NextTab) + " " + name(k.PrevTab), "next / previous tab"}, {name(k.Left) + " " + name(k.Right), "choose, switch tab"},
			{name(k.Quit), "back, quit on stages"}, {name(k.Back), "back, clear, cancel"}, {name(k.ForceQuit), "quit"},
			{name(k.Help), "this help"},
		}},
		{"stage", [][2]string{
			{name(k.Deploy), "plan a deploy"}, {name(k.Apply), "apply the plan"}, {name(k.Refresh) + " on a plan", "plan again from aws"},
			{name(k.View), "a change's diff"}, {name(k.Remove), "remove the stage"}, {name(k.Unlock), "release a lock"},
			{name(k.Yes) + " " + name(k.No), "answer a dialog"}, {name(k.Filter), "filter a table"}, {name(k.Fold), "fold a group"},
			{name(k.GroupBy) + " on a plan", "by kind / action"}, {name(k.Copy), "copy"},
			{name(k.Refresh) + " " + name(k.Pause), "refresh / pause"},
		}},
		{"function", [][2]string{
			{name(k.Invoke), "invoke in aws"}, {name(k.RunLocal), "run on this machine"}, {name(k.EditEvent), "edit the event"},
			{name(k.Deploy), "plan its code"}, {name(k.Follow) + " " + name(k.Filter), "follow, filter logs"},
			{"↑ at top", "older logs"}, {name(k.Level) + " " + name(k.Group) + " " + name(k.Stamps), "level, group, time"},
			{name(k.View), "open the row"}, {name(k.Next) + " " + name(k.Previous) + " " + name(k.Request), "step, its invocation"},
			{name(k.Pane), "payload / result"},
		}},
		{"diff", [][2]string{
			{name(k.NextDiff) + " " + name(k.PrevDiff), "next / previous change"}, {name(k.View), "unfold the lines in sight"},
			{name(k.Unfold), "unfold or fold all"},
		}},
		{"json", [][2]string{
			{name(k.Fold), "fold / unfold"}, {name(k.Unfold), "unfold or fold all"},
			{name(k.Filter), "search"}, {name(k.NextMatch) + " " + name(k.PrevMatch), "next / previous match"},
			{name(k.Copy), "copy the value"}, {name(k.CopyPath), "copy its path"},
		}},
	}
}

// helpNotes are what the key reference says below the keys, when there is room.
var helpNotes = []string{
	"● marks the project's app: d plans it and shift+i runs it here",
	"events to invoke with are kept in .koldstart/events/<function>/",
}

// helpLayout is one way to draw the key reference: which sections, the gap between them, how
// much padding, and whether the notes and the line on closing it are shown.
type helpLayout struct {
	sections   []helpSection
	gap        int
	padY, padX int
	notes      bool
	close      bool
}

// helpView draws the key reference in a card, in the roomiest layout the content area holds.
// Where every section does not fit, it shows three: the keys that work everywhere, the stage's,
// and the function's, or the diff's on a diff and the json's on a resource, and then two: the
// keys that work everywhere beside the ones for the page on screen. The notes are left out before
// any keys are.
func (m model) helpView() string {
	all := helpSections(m.keys)
	pair := []helpSection{all[0], all[1]}
	switch m.page {
	case functionPage, runLocalPage, invocationPage, logLinePage:
		pair[1] = all[2]
	case diffPage:
		pair[1] = all[3]
	case resourcePage, outputPage, inputsPage:
		pair[1] = all[4]
	}
	three := all[:3]
	switch m.page {
	case diffPage:
		three = []helpSection{all[0], all[1], all[3]}
	case resourcePage, outputPage, inputsPage:
		three = []helpSection{all[0], all[1], all[4]}
	case logLinePage:
		three = []helpSection{all[0], all[2], all[4]}
	}
	layouts := []helpLayout{
		{sections: all, gap: 4, padY: 1, padX: 3, notes: true, close: true},
		{sections: all, gap: 4, padX: 2, notes: true},
		{sections: all, gap: 3, padX: 1},
		{sections: three, gap: 4, padY: 1, padX: 3, notes: true, close: true},
		{sections: three, gap: 4, padX: 2, notes: true},
		{sections: three, gap: 3, padX: 1},
		{sections: pair, gap: 4, padY: 1, padX: 3, notes: true, close: true},
		{sections: pair, gap: 4, padX: 2, notes: true},
		{sections: pair, gap: 3, padX: 1},
	}
	var body string
	var last helpLayout
	for _, l := range layouts {
		body, last = l.body(), l
		if lipgloss.Width(body) <= l.width() && lipgloss.Height(body) <= l.height() {
			break
		}
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(styles.P.RuleBold).Padding(last.padY, last.padX).
		Render(clip(body, last.width(), last.height()))
}

// width is the room inside the card, once the overlay's margin, the border and the padding are
// taken from the content area.
func (l helpLayout) width() int { return contentWidth - 4 - 2*l.padX }

// height is the room inside the card, once the border and the padding are taken from the content
// area.
func (l helpLayout) height() int { return contentHeight - 2 - 2*l.padY }

// body draws the sections side by side, each key in a column as wide as its longest, with the
// notes below.
func (l helpLayout) body() string {
	columns := make([]string, 0, len(l.sections))
	for i, s := range l.sections {
		width := 0
		for _, r := range s.rows {
			width = max(width, ansi.StringWidth(r[0]))
		}
		keyStyle := styles.Fg(styles.ToneText).Bold(true).Width(width + 2)
		lines := []string{styles.Fg(styles.ToneAccent).Bold(true).MarginBottom(1).Render(s.title)}
		for _, r := range s.rows {
			lines = append(lines, keyStyle.Render(r[0])+styles.Muted(r[1]))
		}
		column := lipgloss.JoinVertical(lipgloss.Left, lines...)
		if i < len(l.sections)-1 {
			column = lipgloss.NewStyle().MarginRight(l.gap).Render(column)
		}
		columns = append(columns, column)
	}
	parts := []string{lipgloss.JoinHorizontal(lipgloss.Top, columns...)}
	if l.notes {
		parts = append(parts, "")
		for _, n := range helpNotes {
			parts = append(parts, styles.Faint(n))
		}
	}
	if l.close {
		parts = append(parts, "", styles.Faint("press any key to close"))
	}
	return lipgloss.JoinVertical(lipgloss.Center, parts...)
}

// restyleInputs gives every text input the palette in use now, once the terminal has said what
// its background and colours are. Styling an input resets its cursor's blink, so it is done
// when the palette changes and not on every frame.
func (m model) restyleInputs() model {
	in := styles.TextInput()
	m.state.overview.filter.input.SetStyles(in)
	m.state.stage.filter.input.SetStyles(in)
	m.state.function.logs.filter.input.SetStyles(in)
	m.state.remove.confirm.input.SetStyles(in)
	m.state.resource.applied.search.input.SetStyles(in)
	m.state.resource.props.search.input.SetStyles(in)
	m.state.output.json.search.input.SetStyles(in)
	m.state.function.pane.view.search.input.SetStyles(in)
	m.state.inputs.json.search.input.SetStyles(in)
	m.state.logLine.json.search.input.SetStyles(in)
	return m
}
