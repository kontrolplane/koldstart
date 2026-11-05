// Package tui is the terminal interface of kdst ui. It follows relay (kontrolplane/relay): one
// model with a page enum and a state struct per page, relay's palette, and loads that run as
// commands with a timeout, their messages carrying the generation they were asked for.
package tui

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// binary is the name the binary was run by, kdst or koldstart, for the commands the interface
// suggests.
var binary = cli.Name(os.Args[0])

// backend is what the interface reads and changes, a *cli.Console outside tests. EventsDir is
// "" without a project here to keep events in.
type backend interface {
	Identity() cli.Identity
	Project() (app, stage string, err error)
	Local(ref cli.StageRef) bool
	EventsDir(function string) string
	Stages(ctx context.Context) ([]cli.StageSummary, error)
	Stage(ctx context.Context, ref cli.StageRef) (*state.State, *state.LockInfo, error)
	History(ctx context.Context, ref cli.StageRef) ([]state.ObjectVersion, error)
	Version(ctx context.Context, ref cli.StageRef, id string) (*state.State, error)
	LogsBetween(ctx context.Context, ref cli.StageRef, group string, since, until time.Time, filter string) ([]cli.LogEvent, error)
	Tail(ctx context.Context, ref cli.StageRef, groupARN string, fn func([]cli.LogEvent)) error
	Live(ctx context.Context, ref cli.StageRef, typ, physicalID string) (map[string]any, error)
	Function(ctx context.Context, ref cli.StageRef, name string) (cli.FunctionState, error)
	Queue(ctx context.Context, ref cli.StageRef, url string) (cli.QueueDepth, error)
	Metrics(ctx context.Context, ref cli.StageRef, names []string) (map[string]cli.Metrics, error)
	Invoke(ctx context.Context, ref cli.StageRef, function string, payload []byte) (cli.Invocation, error)
	InvokeLocal(ctx context.Context, ref cli.StageRef, function string, payload []byte) (cli.Invocation, error)
	Plan(ctx context.Context, ref cli.StageRef, function string, refresh bool) (*engine.Plan, error)
	Deploy(ctx context.Context, ref cli.StageRef, function string, shown *engine.Plan, onEvent func(engine.Event)) (*engine.Plan, bool, error)
	PlanRemove(ctx context.Context, ref cli.StageRef) (*engine.Plan, error)
	Remove(ctx context.Context, ref cli.StageRef, shown *engine.Plan, onEvent func(engine.Event)) (*engine.Plan, bool, bool, error)
	Unlock(ctx context.Context, ref cli.StageRef) error
}

// operations are the deploys and removes in flight, which quitting stops and waits for, so their
// locks are released and the state says what was done.
type operations struct {
	sync.WaitGroup
	mu      sync.Mutex
	running map[string]bool
}

func (o *operations) start(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Add(1)
	o.running[name] = true
}

func (o *operations) done(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.running, name)
	o.Done()
}

func (o *operations) names() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Sorted(maps.Keys(o.running))
}

// Run opens the interface on c, in the theme of --theme, until it is quit. A deploy or remove
// still running then is stopped, and waited for.
func Run(ctx context.Context, c *cli.Console, theme string) error {
	useTheme(theme)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, c)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	m.ops.stop(os.Stderr, cancel)
	if err != nil {
		return fmt.Errorf("running the terminal interface: %w", err)
	}
	return nil
}

// stop cancels the operations still running and waits for them, saying on w what it waits for
// first, as the calls in flight can take a while to answer once the interface is gone.
func (o *operations) stop(w io.Writer, cancel context.CancelFunc) {
	stopped := o.names()
	cancel()
	for _, name := range stopped {
		_, _ = fmt.Fprintf(w, "stopping %s: waiting for the calls to aws in flight, then releasing its lock…\n", name)
	}
	o.Wait()
	for _, name := range stopped {
		_, _ = fmt.Fprintf(w, "stopped %s: what was done is in the state, run it again to finish\n", name)
	}
}

// useTheme picks the palette: dark and light paint their own background, and auto starts from
// what COLORFGBG says of the terminal's, dark without it, until the terminal answers Init's query
// of its background. Asking within the program, not before it, means a terminal that does not
// answer, as some multiplexers do not, costs no wait and leaves no reply to echo.
func useTheme(theme string) {
	switch theme {
	case "dark":
		styles.Use(true)
		styles.Paint = true
	case "light":
		styles.Use(false)
		styles.Paint = true
	default:
		styles.Use(darkGround(os.Getenv("COLORFGBG")))
		styles.Paint = false
	}
}

// darkGround reports whether COLORFGBG, as rxvt, konsole and tmux set it, names a dark
// background: its last field is the colour of the background, 0 to 6 and 8 dark. Anything else,
// or nothing, is taken as dark, as most terminals are.
func darkGround(colorfgbg string) bool {
	fields := strings.Split(colorfgbg, ";")
	bg, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil || bg < 0 || bg > 15 {
		return true
	}
	return bg <= 6 || bg == 8
}
