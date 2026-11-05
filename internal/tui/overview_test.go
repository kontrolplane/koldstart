package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
)

// TestOverviewEmpty checks the overview says why it has no rows, and what changes that.
func TestOverviewEmpty(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		none   bool
		want   string
	}{
		{name: "no stage matches the filter", filter: "zzz", want: "no stage matches zzz: esc clears the filter"},
		{name: "no stages in the account", none: true, want: "no stages in this account yet: deploy one with " + binary + " deploy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.app = ""
			if tt.none {
				f.states = map[cli.StageRef]*state.State{}
			}
			m := started(t, f)
			if tt.filter != "" {
				m = typeText(t, pressKeys(t, m, "/"), tt.filter)
			}
			if v := screen(m); !strings.Contains(v, tt.want) {
				t.Errorf("the overview does not say %q:\n%s", tt.want, v)
			}
		})
	}
}

// TestOverviewLoadFails checks a first load that fails raises the error once, says on the page
// that r tries again, and that the refreshes failing after it go to the footer instead.
func TestOverviewLoadFails(t *testing.T) {
	f := newFake(t)
	f.app, f.err = "", errAWS
	m := started(t, f)
	if !strings.Contains(m.error, "aws said no") {
		t.Fatalf("error %q, want the failed load", m.error)
	}
	m = pressKeys(t, m, "x")
	if v := screen(m); !strings.Contains(v, "could not list the stages: r tries again") {
		t.Errorf("the page does not say the load failed:\n%s", v)
	}
	m, cmd := update(t, m, refreshTickMsg{gen: m.refreshGen})
	m = settle(t, m, cmd)
	if m.error != "" || !strings.Contains(m.status, "aws said no") {
		t.Errorf("a failed refresh: error %q, status %q; want the error in the footer", m.error, m.status)
	}
	f.err = nil
	m = pressKeys(t, m, "r")
	if len(m.state.overview.shown) != 3 {
		t.Errorf("after r: %d stages, want 3", len(m.state.overview.shown))
	}
}

// TestProjectStageInOrder checks the project's stage before its first deploy is listed where it
// sorts, by app, stage and region.
func TestProjectStageInOrder(t *testing.T) {
	tests := []struct {
		name  string
		stage string
		want  []string
	}{
		{name: "between two stages", stage: "pr-12", want: []string{"billing/dev", "orders/dev", "orders/pr-12", "orders/prod"}},
		{name: "first", stage: "a", want: []string{"billing/dev", "orders/a", "orders/dev", "orders/prod"}},
		{name: "last", stage: "zz", want: []string{"billing/dev", "orders/dev", "orders/prod", "orders/zz"}},
		{name: "deployed already", stage: "dev", want: []string{"billing/dev", "orders/dev", "orders/prod"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.stage = tt.stage
			m := pressKeys(t, started(t, f), "q")
			got := make([]string, 0, len(m.state.overview.shown))
			for _, s := range m.state.overview.shown {
				got = append(got, s.String())
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("stages %v, want %v", got, tt.want)
			}
		})
	}
}

// TestOverviewHints checks the footer offers d only on the project's app, ctrl+x only on a stage
// that is locked, and ctrl+d only on one that is deployed.
func TestOverviewHints(t *testing.T) {
	tests := []struct {
		name  string
		row   int
		hints []string
		not   []string
	}{
		{name: "another app", row: 0, hints: []string{"enter open", "ctrl+d remove"}, not: []string{"d plan", "ctrl+x unlock"}},
		{name: "the project's app", row: 1, hints: []string{"d plan"}, not: []string{"ctrl+x unlock"}},
		{name: "not deployed yet", row: 2, hints: []string{"d plan"}, not: []string{"ctrl+d remove"}},
		{name: "locked", row: 3, hints: []string{"d plan", "ctrl+x unlock"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.stage = "pr-12"
			f.locks[ordersProd] = &state.LockInfo{Who: "ci", Operation: "deploy", Since: time.Now()}
			m := pressKeys(t, started(t, f), "q")
			m.state.overview.table.setCursor(tt.row)
			footer := ansi.Strip(m.footer())
			for _, h := range tt.hints {
				if !strings.Contains(footer, h) {
					t.Errorf("the footer %q does not offer %q", footer, h)
				}
			}
			for _, h := range tt.not {
				if strings.Contains(footer, h) {
					t.Errorf("the footer %q offers %q", footer, h)
				}
			}
		})
	}
}

// TestPauseHint checks the footer offers p to pause, and to resume once paused.
func TestPauseHint(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "q")
	if !strings.Contains(ansi.Strip(m.footer()), "p pause") {
		t.Errorf("the footer %q does not offer to pause", ansi.Strip(m.footer()))
	}
	m = pressKeys(t, m, "p")
	if !strings.Contains(ansi.Strip(m.footer()), "p resume") {
		t.Errorf("paused, the footer %q does not offer to resume", ansi.Strip(m.footer()))
	}
}

// TestLoadingHints checks that while a page loads the footer offers only what works then.
func TestLoadingHints(t *testing.T) {
	m := newTestModel(t, newFake(t))
	if got, want := ansi.Strip(m.footer()), "? help  ·  q quit"; strings.TrimSpace(got) != want {
		t.Errorf("footer while loading %q, want %q", got, want)
	}
}

// TestCallerName checks the header names the caller by the end of its arn.
func TestCallerName(t *testing.T) {
	tests := []struct {
		name string
		arn  string
		want string
	}{
		{name: "a user", arn: "arn:aws:iam::123456789012:user/levi", want: "user/levi"},
		{name: "an assumed role", arn: "arn:aws:sts::123456789012:assumed-role/admin/levi", want: "assumed-role/admin/levi"},
		{name: "the root user", arn: "arn:aws-cn:iam::123456789012:root", want: "root"},
		{name: "not an arn", arn: "levi", want: "levi"},
		{name: "empty", arn: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := callerName(tt.arn); got != tt.want {
				t.Errorf("callerName(%q) = %q, want %q", tt.arn, got, tt.want)
			}
		})
	}
}

// TestHelpFits opens the help at terminal sizes from the smallest it draws at, and checks no key
// is cut off, and that the keys of the page on screen are there.
func TestHelpFits(t *testing.T) {
	sizes := [][2]int{{78, 22}, {80, 24}, {100, 30}, {120, 30}, {200, 60}}
	for _, size := range sizes {
		m := newTestModel(t, newFake(t))
		m, _ = update(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = settle(t, m, m.Init())
		for name, want := range map[string]string{"stage": "plan a deploy", "function": "invoke in aws"} {
			p := m
			if name == "function" {
				p = pressKeys(t, p, "enter")
			}
			p.showHelp = true
			v := ansi.Strip(p.helpView())
			if strings.Contains(v, "…") {
				t.Errorf("%dx%d on the %s: the help is cut:\n%s", size[0], size[1], name, v)
			}
			for _, w := range []string{"back, quit on stages", "quit", want} {
				if !strings.Contains(v, w) {
					t.Errorf("%dx%d on the %s: the help does not say %q:\n%s", size[0], size[1], name, w, v)
				}
			}
		}
	}
}

// TestHelpNamesEveryKey checks every binding of the key map is in the help, but for the ones the
// help names in a pair of its own and ? itself.
func TestHelpNamesEveryKey(t *testing.T) {
	k := newTestModel(t, newFake(t)).keys
	var named []string
	for _, s := range helpSections(k) {
		for _, r := range s.rows {
			named = append(named, r[0])
		}
	}
	all := strings.Join(named, "\n")
	for _, b := range []key.Binding{k.PageUp, k.PageDown, k.Top, k.Bottom, k.View, k.Filter, k.NextTab, k.PrevTab, k.Deploy, k.Apply,
		k.Remove, k.Unlock, k.Invoke, k.RunLocal, k.EditEvent, k.Follow, k.Copy, k.Refresh, k.Pause, k.Yes, k.No, k.Back, k.Quit, k.ForceQuit} {
		if !strings.Contains(all, b.Help().Key) {
			t.Errorf("the help does not name %s, %s", b.Help().Key, b.Help().Desc)
		}
	}
}
