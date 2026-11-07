package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// sdAt is the interface on f at w by h, once its first loads are in.
func sdAt(t *testing.T, f *fakeBackend, w, h int) model {
	t.Helper()
	m := newModel(t.Context(), f)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	return settle(t, m, m.Init())
}

// sdLine is the line on screen that holds want, and the column it starts at, or -1.
func sdLine(m model, want string) (string, int) {
	for line := range strings.SplitSeq(screen(m), "\n") {
		if before, _, ok := strings.Cut(line, want); ok {
			return line, ansi.StringWidth(before)
		}
	}
	return "", -1
}

// sdBorder is the frame's top border, with the breadcrumb and what is beside it.
func sdBorder(m model) string {
	line, _ := sdLine(m, "stages ›")
	return line
}

// TestStageHead checks the facts of the stage in the frame's top border, beside the breadcrumb
// that names it: its resources and what a remove kept, when it was deployed and by whom, and its
// lock, without the account and region the header shows, the shorter forms where the whole line
// does not fit, and the lock alone where those do not either.
func TestStageHead(t *testing.T) {
	tests := []struct {
		name  string
		width int
		prep  func(f *fakeBackend)
		facts []string
		not   []string
	}{
		{
			name: "deployed and locked", width: 200,
			facts: []string{"200 resources · 1 kept · deployed 3h ago by levi · " + formatTime(time.Now().Add(-3 * time.Hour))[:9], " · ● ana on ana-mbp · deploy since ", ", 4m ago"},
			not:   []string{"123456789012", "eu-west-1"},
		},
		{
			name: "narrow", width: 80,
			facts: []string{"● ana on ana-mbp, 4m ago"},
			not:   []string{"deploy since", "123456789012", "eu-west-1"},
		},
		{
			name: "free", width: 120, prep: func(f *fakeBackend) { delete(f.locks, ordersDev) },
			facts: []string{"deployed 3h ago by levi · ", " · lock free"},
		},
		{
			name: "a lock that says little", width: 120, prep: func(f *fakeBackend) { f.locks[ordersDev] = &state.LockInfo{} },
			facts: []string{" · ● someone"},
			not:   []string{"since"},
		},
		{
			name: "not deployed yet", width: 140,
			prep: func(f *fakeBackend) {
				st := state.New("orders", "dev", "eu-west-1", "123456789012")
				f.states[ordersDev] = st
			},
			facts: []string{"0 resources · not deployed yet · d plans its first deploy · ● ana on ana-mbp"},
			not:   []string{"kept"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := sdStage(t, 3)
			if tt.prep != nil {
				tt.prep(f)
			}
			m := sdAt(t, f, tt.width, 30)
			border, at := sdLine(m, "stages › app orders › stage dev")
			if at < 0 {
				t.Fatalf("no breadcrumb of orders/dev in:\n%s", screen(m))
			}
			for _, want := range tt.facts {
				if !strings.Contains(border, want) {
					t.Errorf("the border has no %q: %s", want, border)
				}
			}
			for _, not := range tt.not {
				if strings.Contains(border, not) {
					t.Errorf("the border says %q: %s", not, border)
				}
			}
			if strings.Contains(border, "…") {
				t.Errorf("the facts are cut: %s", border)
			}
		})
	}
}

// TestTypeName checks a type is shown as a person says it, its service left out where the name
// starts with it.
func TestTypeName(t *testing.T) {
	tests := []struct {
		typ  string
		want string
	}{
		{typ: "AWS::Lambda::Function", want: "lambda function"},
		{typ: "koldstart::Lambda::Function", want: "lambda function"},
		{typ: "koldstart::Logs::LogGroup", want: "log group"},
		{typ: "koldstart::IAM::Role", want: "iam role"},
		{typ: "koldstart::Lambda::EventSourceMapping", want: "lambda event source mapping"},
		{typ: "koldstart::ApiGatewayV2::DomainName", want: "api gateway domain name"},
		{typ: "koldstart::ApiGatewayV2::Api", want: "api gateway api"},
		{typ: "koldstart::ElasticLoadBalancingV2::TargetGroup", want: "load balancer target group"},
		{typ: "koldstart::Events::Rule", want: "eventbridge rule"},
		{typ: "AWS::RDS::DBCluster", want: "rds db cluster"},
		{typ: "AWS::S3::Bucket", want: "s3 bucket"},
		{typ: "AWS::SQS::Queue", want: "sqs queue"},
		{typ: "AWS::EC2::VPC", want: "ec2 vpc"},
		{typ: "Custom", want: "custom"},
		{typ: "", want: ""},
		{typ: "AWS::SQS::", want: "sqs"},
		{typ: "AWS::X::日本Name", want: "x 日本 name"},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			if got := typeName(tt.typ); got != tt.want {
				t.Errorf("typeName(%q) = %q, want %q", tt.typ, got, tt.want)
			}
		})
	}
}

// sdTree is the names of the rows of the resources tab, as drawn.
func sdTree(m model) []string {
	s := m.state.stage
	out := make([]string, 0, len(s.tables[resourcesTab].rows))
	for _, r := range s.tables[resourcesTab].rows {
		out = append(out, plainText(r[0]))
	}
	return out
}

// TestResourceFold checks space folds the group under the cursor, from its head or what it
// brings, with the cursor on its head and how many it hides, and opens it again; on the heading of
// a kind it folds every group of the kind, and opens them all; and while the filter holds text,
// every group is open.
func TestResourceFold(t *testing.T) {
	tests := []struct {
		name   string
		to     string
		keys   []string
		want   []string
		cursor string
	}{
		{name: "open", keys: []string{"j"}, want: []string{"functions  3", "▾ api", "  ├─ integration/api", "  └─ url/api", "▾ checkout"}, cursor: "function/api"},
		{name: "a head", keys: []string{"j", "space"}, want: []string{"functions  3", "▸ api  +6", "▾ checkout"}, cursor: "function/api"},
		{name: "from what it brings", keys: []string{"j", "j", "j", "space"}, want: []string{"▸ api  +6", "▾ checkout"}, cursor: "function/api"},
		{name: "and open again", keys: []string{"j", "space", "space"}, want: []string{"▾ api", "  ├─ integration/api"}, cursor: "function/api"},
		{name: "a kind", keys: []string{"space"}, want: []string{"functions  3", "▸ api  +6", "▸ checkout  +2", "▸ orders-worker  +5", "resources  146"}, cursor: "kind/functions"},
		{name: "a kind by enter", keys: []string{"enter"}, want: []string{"▸ api  +6", "▸ checkout  +2"}, cursor: "kind/functions"},
		{name: "a kind opened", keys: []string{"j", "space", "k", "space", "space"}, want: []string{"▾ api", "▾ checkout"}, cursor: "kind/functions"},
		{name: "a group without members", to: "resource/table", keys: []string{"space"}, want: []string{"  table", "▾ uploads-02"}, cursor: "resource/table"},
		{name: "the http api", to: "domain", keys: []string{"space"}, want: []string{"http api", "▸ httpapi  +2"}, cursor: "httpapi"},
		{name: "a filter opens them", keys: []string{"space", "/", "l", "o", "g", "enter", "space"}, want: []string{"▸ api  +6"}, cursor: "kind/functions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, sdStage(t, 3)), "tab")
			for tt.to != "" && m.state.stage.keys[resourcesTab][m.state.stage.tables[resourcesTab].cursor] != tt.to {
				m = pressKeys(t, m, "j")
			}
			m = pressKeys(t, m, tt.keys...)
			s := m.state.stage
			if got := s.keys[resourcesTab][s.tables[resourcesTab].cursor]; got != tt.cursor {
				t.Errorf("cursor on %s, want %s", got, tt.cursor)
			}
			tree := strings.Join(sdTree(m), "\n")
			if strings.Contains(tt.name, "filter") {
				if !strings.Contains(tree, "├─ loggroup/api") || strings.Contains(tree, "▸") {
					t.Errorf("the filter left groups folded:\n%s", tree)
				}
				m = pressKeys(t, m, "esc")
				tree = strings.Join(sdTree(m), "\n")
			}
			for _, want := range tt.want {
				if !strings.Contains(tree, want) {
					t.Errorf("no %q in the tree:\n%s", want, tree)
				}
			}
		})
	}
}

// TestResourceFoldMarks checks a folded group whose hidden members are pending says so with ▲,
// in warning, and a pending resource in sight says so in its note.
func TestResourceFoldMarks(t *testing.T) {
	f := sdStage(t, 3)
	f.states[ordersDev].Resources["notification/uploads-02"].Pending = &state.Pending{Operation: "create", StartedAt: time.Now()}
	m := pressKeys(t, started(t, f), "tab")
	for _, r := range m.state.stage.tables[resourcesTab].rows {
		if plainText(r[0]) == "  table" {
			if tone, ok := toneOf(r[3], "▲ update pending"); !ok || tone != styles.ToneWarning {
				t.Errorf("the pending table's note: %q", plainText(r[3]))
			}
		}
	}
	for m.state.stage.keys[resourcesTab][m.state.stage.tables[resourcesTab].cursor] != "kind/resources" {
		m = pressKeys(t, m, "j")
	}
	m = pressKeys(t, m, "space")
	for _, r := range m.state.stage.tables[resourcesTab].rows {
		if strings.HasPrefix(plainText(r[0]), "▸ uploads-02") {
			if tone, ok := toneOf(r[0], "▲ +1"); !ok || tone != styles.ToneWarning {
				t.Errorf("the folded bucket: %q", plainText(r[0]))
			}
			return
		}
	}
	t.Errorf("no folded bucket:\n%s", strings.Join(sdTree(m), "\n"))
}

// TestDriftMarks checks a resource or function its own page found changed outside koldstart is
// marked ▲ on the stage, until the state is written again.
func TestDriftMarks(t *testing.T) {
	f := newFake(t)
	f.live = map[string]any{"BillingMode": "PROVISIONED"}
	f.function = cli.FunctionState{State: "Active", Memory: 2048}
	m := pressKeys(t, openTable(t, f), "esc")
	if got := sdRowText(m, len(m.state.stage.tree)-1); !strings.Contains(got, "▲ drifted") {
		t.Errorf("the table after its page found it drifted: %q", got)
	}
	m = pressKeys(t, m, "shift+tab", "enter", "esc")
	if got := sdRowText(m, 0); !strings.HasPrefix(got, "api ▲") {
		t.Errorf("the function after its page found it drifted: %q", got)
	}
	f.mu.Lock()
	written := f.states[ordersDev].Clone()
	written.Serial++
	f.states[ordersDev] = written
	f.mu.Unlock()
	m = pressKeys(t, m, "r")
	if got := sdRowText(m, 0); strings.Contains(got, "▲") {
		t.Errorf("the mark outlived a write of the state: %q", got)
	}
}

// TestMetricsNote checks the note beside the tabs of metrics that could not be read, or are
// from an earlier read, takes the longest form that fits its room rather than one cut short, and
// none where none fits.
func TestMetricsNote(t *testing.T) {
	at := time.Date(2026, 11, 7, 10, 4, 5, 0, time.Local)
	tests := []struct {
		name  string
		stale bool
		width int
		want  string
	}{
		{name: "not read, wide", width: 60, want: "could not read the metrics: aws said no"},
		{name: "not read, narrower", width: 30, want: "could not read the metrics"},
		{name: "not read, narrow", width: 15, want: "no metrics"},
		{name: "not read, no room", width: 9, want: ""},
		{name: "stale, wide", stale: true, width: 80, want: "metrics as of 10:04:05, could not read again: aws said no"},
		{name: "stale, narrower", stale: true, width: 30, want: "metrics as of 10:04:05"},
		{name: "stale, narrow", stale: true, width: 15, want: "as of 10:04:05"},
		{name: "stale, no room", stale: true, width: 13, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m model
			m.state.stage.metricsErr = "aws said no"
			if tt.stale {
				m.state.stage.metrics, m.state.stage.metricsAt = map[string]cli.Metrics{}, at
			}
			got := ansi.Strip(m.metricsNote(tt.width))
			if got != tt.want {
				t.Errorf("metricsNote(%d) = %q, want %q", tt.width, got, tt.want)
			}
			if ansi.StringWidth(got) > tt.width {
				t.Errorf("%q is wider than %d", got, tt.width)
			}
		})
	}
}

// TestStageNoteCentred checks a note set in the middle of the stage, as why its state could not
// be read, is drawn no wider than its text, and where it does not fit on one line, the key that
// ends it starts a line of its own rather than being split.
func TestStageNoteCentred(t *testing.T) {
	note := cell{styles.S("could not read the state of orders/dev: aws said no", styles.ToneDanger), styles.S(" · r tries again", styles.ToneFaint)}
	tests := []struct {
		name  string
		width int
		want  []string
	}{
		{name: "fits", width: 90, want: []string{"could not read the state of orders/dev: aws said no · r tries again"}},
		{name: "does not fit", width: 60, want: []string{"could not read the state of orders/dev: aws said no", "r tries again"}},
		{name: "narrow", width: 30, want: []string{"could not read the state of", "orders/dev: aws said no", "r tries again"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, l := range centredLines(note, tt.width) {
				got = append(got, ansi.Strip(l))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("lines %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStageHints checks the keys the foot offers on the stage are the ones that do something:
// no enter on the heading of a kind of resource while the filter holds text, as nothing folds
// then, no c on a heading, which has nothing to copy, and only r, help and back on a stage whose
// state could not be read.
func TestStageHints(t *testing.T) {
	tests := []struct {
		name string
		prep func(t *testing.T, m model) model
		want []string
		not  []string
	}{
		{
			name: "a function",
			prep: func(_ *testing.T, m model) model { return m },
			want: []string{"enter", "i", "c", "/"},
		},
		{
			name: "a heading",
			prep: func(t *testing.T, m model) model { return pressKeys(t, m, "tab") },
			want: []string{"enter/space", "/"},
			not:  []string{"c", "space"},
		},
		{
			name: "a resource",
			prep: func(t *testing.T, m model) model { return pressKeys(t, m, "tab", "j") },
			want: []string{"enter", "space", "c"},
		},
		{
			name: "a heading while filtered",
			prep: func(t *testing.T, m model) model {
				return pressKeys(t, typeText(t, pressKeys(t, m, "tab", "/"), "api"), "enter")
			},
			want: []string{"tab", "/"},
			not:  []string{"enter", "enter/space", "space", "c"},
		},
		{
			name: "a state not read",
			prep: func(_ *testing.T, m model) model {
				m.state.stage.st, m.state.stage.err = nil, "aws said no"
				return m
			},
			want: []string{"r", "?", "q"},
			not:  []string{"tab", "/", "ctrl+d", "enter"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.prep(t, started(t, sdStage(t, 3)))
			var got []string
			for _, h := range m.stageHints() {
				got = append(got, h[0])
			}
			for _, k := range tt.want {
				if !slices.Contains(got, k) {
					t.Errorf("no %q in %q", k, got)
				}
			}
			for _, k := range tt.not {
				if slices.Contains(got, k) {
					t.Errorf("%q in %q", k, got)
				}
			}
		})
	}
}

// TestFunctionRows checks the functions tab draws the last hour as a sparkline in the accent,
// the minutes with errors in danger, with the errors in danger and bold, and nothing for a
// function not invoked.
func TestFunctionRows(t *testing.T) {
	m := started(t, sdStage(t, 3))
	tests := []struct {
		name   string
		row    int
		tone   styles.Tone
		errors bool
	}{
		{name: "not invoked", row: 0},
		{name: "failing", row: 1, tone: styles.ToneDanger, errors: true},
		{name: "healthy", row: 2, tone: styles.ToneAccent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := m.state.stage.tables[functionsTab].rows[tt.row]
			spark, errs := r[1], r[3]
			switch {
			case tt.tone == 0 && len(spark) != 0:
				t.Errorf("a sparkline %q for a function not invoked", plainText(spark))
			case tt.tone != 0 && (ansi.StringWidth(plainText(spark)) != sparkWidth || !slices.ContainsFunc(spark, func(s styles.Span) bool { return s.Tone == tt.tone })):
				t.Errorf("sparkline %+v, want %d wide with tone %d in it", spark, sparkWidth, tt.tone)
			case tt.tone == styles.ToneAccent && slices.ContainsFunc(spark, func(s styles.Span) bool { return s.Tone == styles.ToneDanger }):
				t.Errorf("sparkline %+v of a function without errors has minutes in danger", spark)
			}
			if got := errs[0].Tone == styles.ToneDanger && errs[0].Bold; got != tt.errors {
				t.Errorf("errors %q in danger and bold %v, want %v", plainText(errs), got, tt.errors)
			}
		})
	}
}

// sdStagePages opens every view of the stage page of f at w by h: each tab, the tree folded and
// at its end, a filter being typed, the outputs opened, and the history at its end.
func sdStagePages(t *testing.T, f *fakeBackend, w, h int) map[string]model {
	t.Helper()
	m := sdAt(t, f, w, h)
	pages := map[string]model{"functions": m}
	res := pressKeys(t, m, "tab")
	pages["resources"] = res
	pages["resources at the end"] = pressKeys(t, res, "G")
	pages["resources folded"] = pressKeys(t, res, "space", "G", "k", "space")
	pages["resources filtered"] = typeText(t, pressKeys(t, res, "/"), "orders-dev")
	out := pressKeys(t, res, "tab")
	pages["outputs"] = out
	for _, name := range []string{"database", "dashboardUrl", "certificate", "queueUrls"} {
		pages["output "+name] = sdOpenOutput(t, out, name)
	}
	pages["output scrolled"] = pressKeys(t, sdOpenOutput(t, out, "database"), "G")
	hist := pressKeys(t, out, "tab")
	pages["history"] = hist
	pages["history at the end"] = pressKeys(t, hist, "G")
	return pages
}

// TestStageLayout draws every view of the stage page, with 3 functions and with 40, from the
// smallest terminal it draws in to a large one, and checks no line is wider than the terminal,
// there are no more lines than it is high, and the head and the row under the cursor are drawn.
func TestStageLayout(t *testing.T) {
	sizes := [][2]int{{78, 22}, {100, 30}, {120, 40}, {200, 60}}
	for _, functions := range []int{3, 40} {
		for _, size := range sizes {
			w, h := size[0], size[1]
			for name, m := range sdStagePages(t, sdStage(t, functions), w, h) {
				lines := strings.Split(m.render(), "\n")
				if len(lines) > h {
					t.Errorf("%d functions, %dx%d %s: %d lines", functions, w, h, name, len(lines))
				}
				for i, line := range lines {
					if lw := ansi.StringWidth(line); lw > w {
						t.Errorf("%d functions, %dx%d %s: line %d is %d wide", functions, w, h, name, i, lw)
						break
					}
				}
				v := screen(m)
				switch {
				case m.page == outputPage && !strings.Contains(v, "holds  "):
					t.Errorf("%d functions, %dx%d %s: no head:\n%s", functions, w, h, name, v)
				case m.page == stagePage && !strings.Contains(name, "filtered") && !strings.Contains(sdBorder(m), "● ana"):
					t.Errorf("%d functions, %dx%d %s: no head:\n%s", functions, w, h, name, v)
				case m.page == stagePage && !strings.Contains(v, "▌"):
					t.Errorf("%d functions, %dx%d %s: the cursor is out of sight:\n%s", functions, w, h, name, v)
				}
			}
		}
	}
}
