package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/spinner"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// The layout. The content area fills the terminal inside the chrome: the header, the frame and
// the footer. Below the minimum a notice asks for a larger terminal.
const (
	tableHeaderRows  = 2
	minTableHeight   = 3
	minContentWidth  = 76
	minContentHeight = 14
	chromeWidth      = 2
	chromeHeight     = 8
)

// loadTimeout bounds every read, and invokeTimeout an invocation, which lambda ends at 15 minutes.
const (
	loadTimeout   = 30 * time.Second
	invokeTimeout = 16 * time.Minute
)

// refreshEvery is how often the page on screen is read again, unless refreshing is paused.
const refreshEvery = 5 * time.Second

// The size of the content area, which setLayout fits to the terminal.
var (
	contentWidth  = 120
	contentHeight = 30
	frameWidth    = contentWidth + chromeWidth
)

func setLayout(width, height int) {
	contentWidth = max(minContentWidth, width-chromeWidth)
	contentHeight = max(minContentHeight, height-chromeHeight)
	frameWidth = contentWidth + chromeWidth
}

type page int

const (
	overviewPage page = iota
	stagePage
	functionPage
	resourcePage
	versionPage
	planPage
	unlockPage
	removePage
	runLocalPage
	diffPage
	invocationPage
	outputPage
)

type model struct {
	ctx  context.Context
	b    backend
	ops  *operations
	keys keys.KeyMap

	ident cli.Identity
	// project is the app of the configuration here, and projectStage the stage of --stage.
	project      string
	projectStage string

	page  page
	state pages

	width, height int
	showHelp      bool
	error         string
	status        string
	statusTone    styles.Tone
	statusGen     int
	loading       bool
	loadingMsg    string
	spinner       spinner.Model
	spinning      bool
	paused        bool
	refreshGen    int
	refreshedAt   time.Time
	// stale says the last read of the page on screen failed, so it shows the one at refreshedAt.
	stale bool
	// gen numbers the loads; a page keeps the one it waits for and drops the others.
	gen int
}

type pages struct {
	overview   overviewState
	stage      stageState
	function   functionState
	resource   resourceState
	version    versionState
	plan       planState
	unlock     unlockState
	remove     removeState
	runLocal   runLocalState
	diff       diffState
	invocation invocationState
	output     outputState
}

func newModel(ctx context.Context, b backend) model {
	m := model{
		ctx:     ctx,
		b:       b,
		ops:     &operations{running: map[string]bool{}},
		keys:    keys.Keys,
		ident:   b.Identity(),
		page:    overviewPage,
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	app, stage, err := b.Project()
	m.project, m.projectStage = app, stage
	if err != nil {
		m.error = "the configuration here did not load, so its stages cannot be deployed: " + err.Error()
	}
	m.state.overview = newOverviewState()
	m.state.stage = newStageState()
	m.loading, m.loadingMsg, m.spinning = true, "listing the stages…", true
	m.state.overview.gen = m.next()
	m.refreshGen = 1
	return m
}

// next numbers a new load.
func (m *model) next() int {
	m.gen++
	return m.gen
}

// withTimeout is the context of a load.
func (m model) withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(m.ctx, d)
}

// switchPage opens a page. A load still running for the page left behind no longer holds the
// spinner.
func (m model) switchPage(p page) model {
	if p != m.page {
		m.refreshedAt, m.stale = time.Time{}, false
	}
	m.page = p
	m.loading = false
	return m
}

// markRead notes the page on screen was read now.
func (m model) markRead() model {
	m.refreshedAt, m.stale = time.Now(), false
	return m
}

// typing reports whether keys go into a text input, so letters are not shortcuts.
func (m model) typing() bool {
	if m.page == removePage {
		return true
	}
	f := m.pageFilter()
	return f != nil && f.active
}

// refreshes reports whether the page on screen reads its data again every few seconds.
func (m model) refreshes() bool {
	switch m.page {
	case overviewPage, stagePage, functionPage:
		return true
	}
	return false
}

// tooSmall reports whether the terminal is smaller than the layout needs.
func (m model) tooSmall() bool {
	return m.width > 0 && (m.width < minContentWidth+chromeWidth || m.height < minContentHeight+chromeHeight)
}

// resize fits the tables of every page to the layout.
func (m model) resize() model {
	m.state.overview.table.setSize(tableWidth(), contentHeight-tableHeaderRows)
	s := &m.state.stage
	for i := range s.tables {
		s.tables[i].setSize(tableWidth(), stageTableHeight())
	}
	f := &m.state.function
	f.events.setSize(eventsWidth(), functionBodyHeight()-tableHeaderRows)
	f.invocations.setSize(tableWidth(), invocationsTableHeight())
	return m
}
