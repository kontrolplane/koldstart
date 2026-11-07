package tui

import (
	"fmt"
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

// TestOverviewLongErrorWraps checks a first load that fails with a long error says all of it on
// a narrow terminal, wrapped under the table's header, with the key that tries again at its end.
func TestOverviewLongErrorWraps(t *testing.T) {
	tests := []struct {
		name  string
		width int
	}{
		{name: "narrow", width: 80},
		{name: "wide", width: 120},
	}
	long := "listing the stages in koldstart-state-123456789012-eu-west-1: operation error S3: ListObjectsV2, https response error StatusCode: 403, api error AccessDenied: Access Denied"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.app, f.err = "", fmt.Errorf("%s", long)
			m := started(t, f)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 24})
			m = pressKeys(t, m, "x")
			v := screen(m)
			var words []string
			for line := range strings.SplitSeq(v, "\n") {
				words = append(words, strings.Fields(strings.Trim(line, "│ "))...)
			}
			joined := strings.Join(words, " ")
			if want := "could not list the stages: " + long + " · r tries again"; !strings.Contains(joined, want) {
				t.Errorf("the page does not say %q:\n%s", want, v)
			}
		})
	}
}

// TestOverviewCounts checks the counts of resources line up at their ends, also where some stages
// say how many resources a remove kept beside them.
func TestOverviewCounts(t *testing.T) {
	tests := []struct {
		name      string
		resources []int
		retained  []int
		want      []string
	}{
		{name: "none kept", resources: []int{4, 140, 12}, retained: []int{0, 0, 0}, want: []string{"  4", "140", " 12"}},
		{name: "some kept", resources: []int{4, 140, 12}, retained: []int{12, 0, 3}, want: []string{"  4 +12 kept", "140         ", " 12  +3 kept"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.app = ""
			m := started(t, f)
			for i := range m.state.overview.stages {
				m.state.overview.stages[i].Resources = tt.resources[i]
				m.state.overview.stages[i].Retained = tt.retained[i]
			}
			m = m.updateOverviewTable()
			lines := strings.Split(screen(m), "\n")
			ends := map[int]bool{}
			for _, w := range tt.want {
				found := false
				for _, l := range lines {
					if i := strings.Index(l, w+"  "); i >= 0 && strings.Contains(l, "ago") {
						ends[ansi.StringWidth(l[:i])+len(w)] = true
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no row with the count %q:\n%s", w, strings.Join(lines, "\n"))
				}
			}
			if len(ends) > 1 {
				t.Errorf("the counts end in %d columns, want one:\n%s", len(ends), strings.Join(lines, "\n"))
			}
		})
	}
}

// TestOverviewLock checks a locked stage names the operation before who holds it, so a narrow
// column cuts the name rather than the operation, and that a stage not yet deployed shows its
// lock too.
func TestOverviewLock(t *testing.T) {
	tests := []struct {
		name  string
		stage string
		row   int
		lock  state.LockInfo
		want  string
	}{
		{name: "a deploy", stage: "dev", row: 1, lock: state.LockInfo{Who: "github-actions[bot]", Operation: "deploy"}, want: "● deploy by github-ac…"},
		{name: "no operation", stage: "dev", row: 1, lock: state.LockInfo{Who: "ci"}, want: "● locked by ci"},
		{name: "no one", stage: "dev", row: 1, lock: state.LockInfo{Operation: "remove"}, want: "● remove by someone"},
		{name: "not deployed yet", stage: "pr-12", row: 2, lock: state.LockInfo{Who: "ci", Operation: "deploy"}, want: "● deploy by ci"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.stage = "pr-12"
			m := pressKeys(t, started(t, f), "q")
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
			lock := tt.lock
			m.state.overview.stages[tt.row].Lock = &lock
			m = m.updateOverviewTable()
			v := screen(m)
			found := false
			for line := range strings.SplitSeq(v, "\n") {
				if strings.Contains(line, " "+tt.stage+" ") && strings.Contains(line, tt.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("the row of %s does not say %q:\n%s", tt.stage, tt.want, v)
			}
		})
	}
}

// TestOverviewPeekNotes checks the peek of a stage it cannot show says why, where the values
// start.
func TestOverviewPeekNotes(t *testing.T) {
	tests := []struct {
		name string
		row  int
		set  func(m *model)
		want string
	}{
		{name: "the project's stage before its first deploy", row: 2, want: "not deployed yet: d plans its first deploy"},
		{name: "a stage that cannot be read", row: 1, set: func(m *model) { m.state.overview.stages[1].Err = errAWS }, want: "could not read the stage: aws said no · r tries again"},
		{name: "a stage being read", row: 3, set: func(m *model) { m.state.overview.peeks = nil }, want: "reading the state of orders/prod…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.stage = "pr-12"
			m := pressKeys(t, started(t, f), "q")
			if tt.set != nil {
				tt.set(&m)
			}
			m = m.updateOverviewTable()
			m.state.overview.table.setCursor(tt.row)
			v := screen(m)
			want := "│" + strings.Repeat(" ", rowInset+labelWidth+1) + tt.want
			if !strings.Contains(v, want) {
				t.Errorf("the peek does not say %q where the values start:\n%s", tt.want, v)
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
	if v := screen(m); !strings.Contains(v, "could not list the stages: aws said no · r tries again") {
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

// TestHeaderFitsBucket checks the header gives a long state bucket the room the columns beside it
// leave, instead of an equal share that cut it while the project's column stood half empty.
func TestHeaderFitsBucket(t *testing.T) {
	for _, width := range []int{100, 120, 200} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := newTestModel(t, newFake(t))
			m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: 30})
			m.ident.Bucket = "koldstart-state-000000000000-ap-southeast-2"
			h := ansi.Strip(m.header())
			for _, want := range []string{m.ident.Bucket, "orders  dev ", "user/levi"} {
				if !strings.Contains(h, want) {
					t.Errorf("the header cuts %q:\n%s", want, h)
				}
			}
			for line := range strings.SplitSeq(h, "\n") {
				if w := ansi.StringWidth(line); w > width {
					t.Errorf("a line of the header is %d wide, wider than %d", w, width)
				}
			}
		})
	}
}

// TestShareWidths checks the widths of the header's columns: equal while each fits its share,
// and otherwise what a narrow column leaves goes to a wide one.
func TestShareWidths(t *testing.T) {
	tests := []struct {
		name  string
		want  []int
		width int
		got   []int
	}{
		{name: "equal shares fit", want: []int{20, 30, 20}, width: 90, got: []int{30, 30, 30}},
		{name: "one wide", want: []int{20, 46, 23}, width: 115, got: []int{28, 54, 31}},
		{name: "not enough", want: []int{20, 46, 23}, width: 75, got: []int{20, 32, 23}},
		{name: "two wide", want: []int{10, 50, 50}, width: 70, got: []int{10, 30, 30}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shareWidths(tt.want, tt.width); fmt.Sprint(got) != fmt.Sprint(tt.got) {
				t.Errorf("shareWidths(%v, %d) = %v, want %v", tt.want, tt.width, got, tt.got)
			}
		})
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
		for name, want := range map[string]string{"stage": "plan a deploy", "function": "invoke in aws", "diff": "next / previous change", "resource": "copy its path"} {
			p := m
			switch name {
			case "function":
				p = pressKeys(t, p, "enter")
			case "diff":
				p, _ = p.openDiff("function/api", "update", map[string]any{"a": 1}, map[string]any{"a": 2})
			case "resource":
				p = pressKeys(t, p, "tab", "G", "enter")
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

// TestHelpNamesEveryKey checks every binding of the key map is in the help, named as its own
// help names it, so a binding added to the key map is not left out of the help.
func TestHelpNamesEveryKey(t *testing.T) {
	k := newTestModel(t, newFake(t)).keys
	named := map[string]bool{}
	for _, s := range helpSections(k) {
		for _, r := range s.rows {
			for word := range strings.FieldsSeq(r[0]) {
				named[word] = true
			}
		}
	}
	v := reflect.ValueOf(k)
	for i := range v.NumField() {
		b, ok := reflect.TypeAssert[key.Binding](v.Field(i))
		if ok && !named[b.Help().Key] {
			t.Errorf("the help does not name %s, %s: %s", v.Type().Field(i).Name, b.Help().Key, b.Help().Desc)
		}
	}
}
