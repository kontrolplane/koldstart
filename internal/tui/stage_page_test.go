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

// TestStageHead checks the head above the tabs, as the function's overview has its own: the
// stage's name, and on one line below its resources and what a remove kept, its account and
// region, when it was deployed and by whom, and its lock, the shorter forms where the whole line
// does not fit, the date, the account and the region the header shows left out.
func TestStageHead(t *testing.T) {
	tests := []struct {
		name  string
		width int
		prep  func(f *fakeBackend)
		facts []string
		not   []string
	}{
		{
			name: "deployed and locked", width: 160,
			facts: []string{"200 resources · 1 kept · 123456789012 · eu-west-1 · deployed 3h ago by levi · " + formatTime(time.Now().Add(-3 * time.Hour))[:9], " · ● ana on ana-mbp · deploy since ", ", 4m ago"},
		},
		{
			name: "narrow", width: 80,
			facts: []string{"200 resources · 1 kept · 3h ago by levi · ● ana on ana-mbp, 4m ago"},
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
			name: "not deployed yet", width: 120,
			prep: func(f *fakeBackend) {
				st := state.New("orders", "dev", "eu-west-1", "123456789012")
				f.states[ordersDev] = st
			},
			facts: []string{"0 resources · 123456789012 · eu-west-1 · not deployed yet · d plans its first deploy · ● ana on ana-mbp, 4m ago"},
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
			title, at := sdLine(m, "orders/dev  ")
			if at != 2 || strings.TrimSpace(strings.Trim(title, "│")) != "orders/dev" {
				t.Fatalf("the title is %q at %d, want orders/dev alone one column in", title, at)
			}
			lines := strings.Split(screen(m), "\n")
			i := slices.Index(lines, title)
			facts := lines[i+1]
			for _, want := range tt.facts {
				if !strings.Contains(facts, want) {
					t.Errorf("the facts have no %q: %s", want, facts)
				}
			}
			for _, not := range tt.not {
				if strings.Contains(facts, not) {
					t.Errorf("the facts say %q: %s", not, facts)
				}
			}
			if strings.Contains(facts, "…") {
				t.Errorf("the facts are cut: %s", facts)
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

// TestFunctionRows checks the functions tab draws the last hour as a sparkline, in danger when
// the hour had errors, with the errors in danger and bold, and nothing for a function not
// invoked.
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
			case tt.tone != 0 && (len(spark) != 1 || spark[0].Tone != tt.tone || ansi.StringWidth(spark[0].Text) != sparkWidth):
				t.Errorf("sparkline %+v, want %d wide in tone %d", spark, sparkWidth, tt.tone)
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
				case m.page == stagePage && (!strings.Contains(v, "│ orders/dev ") || !strings.Contains(v, "● ana")):
					t.Errorf("%d functions, %dx%d %s: no head:\n%s", functions, w, h, name, v)
				case m.page == stagePage && !strings.Contains(v, "▌"):
					t.Errorf("%d functions, %dx%d %s: the cursor is out of sight:\n%s", functions, w, h, name, v)
				}
			}
		}
	}
}
