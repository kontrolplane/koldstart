package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// pdSecret is a secret as the engine leaves it in a plan's inputs.
var pdSecret = map[string]any{"$koldstart": "secret", "source": "ssm", "name": "/orders/api-key", "key": "", "atRuntime": false}

// pdUnknown is a value the engine knows only after apply.
var pdUnknown = map[string]any{"$koldstart": "unknown"}

// pdChange is the i-th change of a plan of every action and kind: functions with their memory,
// environment, refs, secrets and code changed, their roles and routes, tables replaced for their
// key schema, and queues deleted, kept and taken over.
func pdChange(i int) engine.Change {
	actions := []engine.Action{engine.Create, engine.Update, engine.Replace, engine.Delete, engine.Keep, engine.Adopt, engine.NoOp}
	a := actions[i%len(actions)]
	c := engine.Change{Action: a}
	switch i % 5 {
	case 0:
		c.ID, c.Type = fmt.Sprintf("function/fn-%04d", i), "koldstart::Lambda::Function"
		c.Before = map[string]any{"memory": 512.0, "name": "orders-dev-fn", "environment": map[string]any{"TABLE": "orders-dev-orders", "KEY": "plain"},
			"code": map[string]any{"sha256": strings.Repeat("ab", 32)}}
		c.After = map[string]any{"memory": 1024.0, "name": "orders-dev-fn-2", "environment": map[string]any{"TABLE": pdUnknown, "KEY": pdSecret},
			"code": map[string]any{"sha256": strings.Repeat("cd", 32)}}
		c.Fields = []string{"code", "environment", "memory", "name"}
		c.Forces = []string{"name"}
	case 1:
		c.ID, c.Type = fmt.Sprintf("role/fn-%04d", i), "koldstart::IAM::Role"
		c.Before, c.After = map[string]any{"policy": "a"}, map[string]any{"policy": "b"}
		c.Fields = []string{engine.SecretChanged}
	case 2:
		c.ID, c.Type = fmt.Sprintf("route/GET /orders/%04d", i), "koldstart::ApiGatewayV2::Route"
		c.Before, c.After = map[string]any{"target": "integrations/a"}, map[string]any{"target": "integrations/b"}
		c.Fields = []string{"target"}
	case 3:
		c.ID, c.Type = fmt.Sprintf("resource/table-%04d", i), "AWS::DynamoDB::Table"
		c.Before = map[string]any{"properties": map[string]any{"KeySchema": "id", "BillingMode": "PROVISIONED"}}
		c.After = map[string]any{"properties": map[string]any{"KeySchema": "pk", "BillingMode": "PAY_PER_REQUEST"}}
		c.Fields = []string{"properties"}
		c.Forces = []string{"properties.KeySchema"}
		c.Drift = []string{"BillingMode"}
	default:
		c.ID, c.Type = fmt.Sprintf("queue/jobs-%04d", i), "AWS::SQS::Queue"
	}
	return c
}

// pdPlan is a plan of n changes of pdChange.
func pdPlan(n int) *engine.Plan {
	p := &engine.Plan{App: "orders", Stage: "dev"}
	for i := range n {
		p.Changes = append(p.Changes, pdChange(i))
	}
	return p
}

// pdPlanned is the interface on the plan of a deploy of orders/dev with the changes given.
func pdPlanned(t *testing.T, changes ...engine.Change) model {
	t.Helper()
	f := newFake(t)
	f.plan = &engine.Plan{App: "orders", Stage: "dev", Changes: changes}
	m := pressKeys(t, started(t, f), "d")
	if m.page != planPage || m.state.plan.plan == nil {
		t.Fatalf("after d: page %v, plan %v", m.page, m.state.plan.plan)
	}
	return m
}

// pdChoose moves the cursor of the plan to the change id.
func pdChoose(t *testing.T, m model, id string) model {
	t.Helper()
	m = pressKeys(t, m, "g")
	_, _, rows := planRows(m.state.plan)
	for range rows {
		if row, ok := m.planRow(); ok && row.change >= 0 && m.planChanges()[row.change].ID == id {
			return m
		}
		m = pressKeys(t, m, "j")
	}
	t.Fatalf("no row of %s in the plan", id)
	return m
}

// pdHeadings are the headings of the plan's groups, in order, without their marker.
func pdHeadings(m model) []string {
	var out []string
	_, body := m.planLines()
	for _, line := range body.lines.window(0, body.lines.len()) {
		line = ansi.Strip(line)
		t := strings.TrimLeft(line, " ▌")
		if strings.HasPrefix(t, "▾ ") || strings.HasPrefix(t, "▸ ") {
			out = append(out, strings.TrimSpace(t[len("▾ "):]))
		}
	}
	return out
}

// pdByKind are the headings of pdPlan(14) by kind: how many changes each holds, and how many of
// each action of the plan, in columns.
var pdByKind = []string{
	"functions                 3  +1 ~0 -/+0 -1 =0 <1",
	"roles, logs and triggers  2  +0 ~1 -/+0 -0 =1 <0",
	"http api                  3  +1 ~0 -/+1 -0 =0 <1",
	"resources                 2  +0 ~1 -/+0 -1 =0 <0",
	"other                     2  +0 ~0 -/+1 -0 =1 <0",
}

// TestPlanGroups checks the changes are grouped by kind, each heading with how many it holds and
// how many of each action, and o groups them by action, in the order the summary counts them,
// each symbol where its changes have theirs, and back, keeping the cursor on its change.
func TestPlanGroups(t *testing.T) {
	m := pdPlanned(t, pdPlan(14).Changes...)
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{name: "by kind", want: pdByKind},
		{name: "by action", keys: []string{"o"}, want: []string{
			"+   create           2",
			"~   update           2",
			"-/+ replace          2",
			"-   delete           2",
			"=   leave in place   2",
			"<   take over again  2",
		}},
		{name: "by kind again", keys: []string{"o", "o"}, want: pdByKind},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			at := pdChoose(t, m, "route/GET /orders/0002")
			at = pressKeys(t, at, tt.keys...)
			got := pdHeadings(at)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("headings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			if row, ok := at.planRow(); !ok || row.change < 0 || at.planChanges()[row.change].ID != "route/GET /orders/0002" {
				t.Errorf("the cursor left the change it was on: %+v", row)
			}
		})
	}
}

// TestPlanFold checks space folds the group of the change under the cursor to its heading, with
// the cursor on it, and opens it again; enter on a heading does the same; a group folded by kind
// stays open by action.
func TestPlanFold(t *testing.T) {
	m := pdChoose(t, pdPlanned(t, pdPlan(14).Changes...), "resource/table-0008")
	tests := []struct {
		name      string
		keys      []string
		shown     bool
		folded    string
		onHeading bool
	}{
		{name: "space folds", keys: []string{"space"}, folded: "▸ resources", onHeading: true},
		{name: "space opens", keys: []string{"space", "space"}, shown: true},
		{name: "enter on the heading opens", keys: []string{"space", "enter"}, shown: true},
		{name: "open by action", keys: []string{"space", "o"}, shown: true},
		{name: "folded again by kind", keys: []string{"space", "o", "o"}, folded: "▸ resources"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pressKeys(t, m, tt.keys...)
			v := ansi.Strip(got.planView())
			if shown := strings.Contains(v, "resource/table-0008"); shown != tt.shown {
				t.Errorf("the change is shown %v, want %v:\n%s", shown, tt.shown, v)
			}
			if tt.folded != "" && !strings.Contains(v, tt.folded) {
				t.Errorf("no %q:\n%s", tt.folded, v)
			}
			if !tt.onHeading {
				return
			}
			if row, ok := got.planRow(); !ok || row.change >= 0 {
				t.Errorf("the cursor is on %+v, want the heading", row)
			}
			if p := got.planPosition(); p != "12 changes" {
				t.Errorf("position on a heading %q, want how many changes", p)
			}
		})
	}
}

// TestPlanFields checks the fields an update, replace or take over changes are listed under it as
// the text plan has them, one a line: before and after with what changed marked, refs known
// after apply, secrets and code by what they are, at most three and how many more.
func TestPlanFields(t *testing.T) {
	update := engine.Change{ID: "function/api", Action: engine.Update, Fields: []string{"memory"},
		Before: map[string]any{"memory": 512.0}, After: map[string]any{"memory": 1024.0}}
	many := pdChange(0)
	many.Action, many.Forces = engine.Update, nil
	tests := []struct {
		name   string
		change engine.Change
		want   []string
		not    []string
	}{
		{name: "a value", change: update, want: []string{"memory 512 → 1024"}},
		{name: "more than three", change: many, want: []string{"code changed  abababababab → cdcdcdcdcdcd", "environment.KEY (a secret)", "environment.TABLE known after apply", "+2 more · enter for the diff"}, not: []string{"memory 512"}},
		{name: "a secret", change: engine.Change{ID: "function/api", Action: engine.Update, Fields: []string{engine.SecretChanged}}, want: []string{"a secret or another stage's output changed"}},
		{name: "a create", change: engine.Change{ID: "resource/q", Action: engine.Create, After: map[string]any{"a": 1.0}}, not: []string{"a 1", "more"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := ansi.Strip(pdPlanned(t, tt.change).planView())
			for _, w := range tt.want {
				if !strings.Contains(v, w) {
					t.Errorf("the plan does not say %q:\n%s", w, v)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(v, n) {
					t.Errorf("the plan says %q:\n%s", n, v)
				}
			}
		})
	}
}

// TestPlanFieldMarks checks the part of a value that changed is marked as the diff marks it, on
// both sides.
func TestPlanFieldMarks(t *testing.T) {
	m := pdPlanned(t, engine.Change{ID: "function/api", Action: engine.Update, Fields: []string{"role"},
		Before: map[string]any{"role": "arn:aws:iam::1:role/orders-dev"}, After: map[string]any{"role": "arn:aws:iam::1:role/orders-prod"}})
	v := m.planView()
	for _, want := range []string{markStyle(styles.ToneDanger).Render("dev"), markStyle(styles.ToneSuccess).Render("prod")} {
		if !strings.Contains(v, want) {
			t.Errorf("the changed part is not marked %q:\n%q", want, v)
		}
	}
}

// TestPlanReplace checks a replace says which field needs the new resource, in warning, and of a
// table that the old one is deleted with its data, in danger, or left in place when retained.
func TestPlanReplace(t *testing.T) {
	table := pdChange(3 + 5*3)
	table.Action = engine.Replace
	retained := table
	retained.Retained = true
	fn := pdChange(0)
	fn.Action = engine.Replace
	tests := []struct {
		name   string
		change engine.Change
		want   []string
		not    string
	}{
		{name: "a table", change: table, want: []string{"▲ changing properties.KeySchema needs a new resource", "✗ the old one is deleted, and the data it holds with it", "▲ changed outside koldstart: BillingMode"}},
		{name: "retained", change: retained, want: []string{"▲ changing properties.KeySchema needs a new resource", "the old one stays in place beside the new one, as it is retained"}, not: "✗ the old one"},
		{name: "a function", change: fn, want: []string{"▲ changing name needs a new resource"}, not: "the old one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.change.Action != engine.Replace {
				t.Fatalf("%s is a %s", tt.change.ID, tt.change.Action)
			}
			v := ansi.Strip(pdPlanned(t, tt.change).planView())
			for _, w := range tt.want {
				if !strings.Contains(v, w) {
					t.Errorf("the plan does not say %q:\n%s", w, v)
				}
			}
			if tt.not != "" && strings.Contains(v, tt.not) {
				t.Errorf("the plan says %q:\n%s", tt.not, v)
			}
		})
	}
}

// TestPlanHead checks the head names what the plan is of, its counts and when it was made.
func TestPlanHead(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{name: "the stage", keys: []string{"d"}, want: []string{"deploy of orders/dev in eu-west-1", "planned at ", "1 to create, 1 to update"}},
		{name: "a function", keys: []string{"enter", "d"}, want: []string{"deploy of the code of api to orders/dev in eu-west-1"}},
		{name: "read from aws", keys: []string{"d", "r"}, want: []string{"with every resource read as it is in aws"}},
		{name: "a remove", keys: []string{"ctrl+d"}, want: []string{"remove of orders/dev in eu-west-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			if tt.name == "read from aws" {
				f.plan.Refreshed = true
			}
			m := pressKeys(t, started(t, f), tt.keys...)
			v := ansi.Strip(m.planView())
			for _, w := range tt.want {
				if !strings.Contains(v, w) {
					t.Errorf("the head does not say %q:\n%s", w, v)
				}
			}
		})
	}
}

// TestPlanHeadingsLineUp checks the totals of the headings are right aligned and each count by
// action is padded to the widest of its column, so the counts of the groups line up.
func TestPlanHeadingsLineUp(t *testing.T) {
	var many []engine.Change
	for i := range 10 {
		many = append(many, engine.Change{ID: fmt.Sprintf("function/fn-%02d", i), Action: engine.Create})
	}
	route := engine.Change{ID: "route/GET /", Action: engine.Update}
	tests := []struct {
		name    string
		changes []engine.Change
		want    []string
	}{
		{name: "counts of two digits", changes: append(slices.Clone(many), route), want: []string{"functions  10  +10 ~0", "http api    1  +0  ~1"}},
		{name: "one action", changes: []engine.Change{many[0], {ID: "resource/q", Action: engine.Create}}, want: []string{"functions  1  +1", "resources  1  +1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pdHeadings(pdPlanned(t, tt.changes...))
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("headings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// TestPlanRowsEndWithTheHead checks the rows of a plan end in the column its head does, the
// progress of a change under the time it was planned at, at every size.
func TestPlanRowsEndWithTheHead(t *testing.T) {
	tests := []struct {
		name string
		w, h int
	}{
		{name: "small", w: 80, h: 24},
		{name: "medium", w: 120, h: 40},
		{name: "large", w: 200, h: 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pdPlanned(t, pdPlan(3).Changes...)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: tt.w, Height: tt.h})
			m = applying(t, m)
			ends := map[string]int{}
			for line := range strings.SplitSeq(screen(m), "\n") {
				end := ansi.StringWidth(strings.TrimRight(line, " │"))
				switch {
				case strings.Contains(line, "planned at"):
					ends["head"] = end
				case strings.Contains(line, "function/fn-0000"):
					ends["change"] = end
				case strings.Contains(line, "▾ functions"):
					ends["heading"] = end
				}
			}
			if ends["head"] == 0 || ends["change"] != ends["head"] || ends["heading"] != ends["head"] {
				t.Errorf("the head ends in column %d, a change in %d and a heading in %d:\n%s", ends["head"], ends["change"], ends["heading"], screen(m))
			}
		})
	}
}

// TestPlanLongID checks a change whose id is too long for its line is cut in the middle, keeping
// the end of its id and its type in sight, planned and applying, and the change line takes the
// width of the progress column until the apply needs it.
func TestPlanLongID(t *testing.T) {
	long := engine.Change{ID: "resource/" + strings.Repeat("very-long-name-", 10) + "end", Action: engine.Delete, Type: "AWS::DynamoDB::Table"}
	tests := []struct {
		name     string
		applying bool
	}{
		{name: "planned"},
		{name: "applying", applying: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pdPlanned(t, long)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
			if tt.applying {
				m = applying(t, m)
			}
			line := ""
			for l := range strings.SplitSeq(screen(m), "\n") {
				if strings.Contains(l, "dynamodb table") {
					line = l
				}
			}
			for _, want := range []string{"   -   resour", "…", "-end  dynamodb table"} {
				if !strings.Contains(line, want) {
					t.Errorf("the line does not say %q: %q", want, line)
				}
			}
			if waiting := strings.Contains(line, "waiting"); waiting != tt.applying {
				t.Errorf("the line says waiting %v, want %v: %q", waiting, tt.applying, line)
			}
		})
	}
}

// TestPlanGroupProgress checks a group's heading counts its changes done while applying, ✓ once
// all are, and how many failed, each ending in the count done so the counts line up.
func TestPlanGroupProgress(t *testing.T) {
	changes := []engine.Change{
		{ID: "resource/a", Action: engine.Create}, {ID: "resource/b", Action: engine.Create}, {ID: "function/api", Action: engine.Update},
	}
	tests := []struct {
		name   string
		events []engine.Event
		ended  bool
		want   []string
	}{
		{name: "none done", want: []string{"resources  2  +2 ~0", "0/2 done", "0/1 done"}},
		{name: "part way", events: []engine.Event{{ID: "resource/a", Action: engine.Create, Done: true}}, want: []string{"1/2 done"}},
		{name: "all done", events: []engine.Event{{ID: "resource/a", Action: engine.Create, Done: true}, {ID: "resource/b", Action: engine.Create, Done: true}}, want: []string{"✓ 2/2 done"}},
		{name: "failed", events: []engine.Event{{ID: "resource/a", Action: engine.Create, Done: true, Err: errAWS}}, want: []string{"✗ 1 failed · 0/2 done"}},
		{name: "unchanged after all", ended: true, events: []engine.Event{{ID: "resource/a", Action: engine.Create, Done: true}}, want: []string{"✓ 2/2 done"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := applying(t, pdPlanned(t, changes...), tt.events...)
			if tt.ended {
				m, _ = update(t, m, appliedMsg{plan: m.state.plan.plan, applied: true})
			}
			v := ansi.Strip(m.planView())
			for _, w := range tt.want {
				if !strings.Contains(v, w) {
					t.Errorf("the plan does not say %q:\n%s", w, v)
				}
			}
		})
	}
}

// TestPlanLayout draws plans of 3 and of 1500 changes, of every action, a table replaced, refs and
// secrets, planned, folded, grouped by action, scrolled to the end, and applying and failed part
// way with errors, at 78 by 22, 120 by 40 and 200 by 60, and checks every line fits.
func TestPlanLayout(t *testing.T) {
	long := errors.New(strings.Repeat("ValidationException: a long reason the call failed ", 8))
	states := map[string]func(t *testing.T, m model) model{
		"planned":   func(_ *testing.T, m model) model { return m },
		"folded":    func(t *testing.T, m model) model { return pressKeys(t, m, "space") },
		"by action": func(t *testing.T, m model) model { return pressKeys(t, m, "o", "G") },
		"at the end": func(t *testing.T, m model) model {
			return pressKeys(t, m, "G")
		},
		"applying": func(t *testing.T, m model) model {
			var events []engine.Event
			for i, c := range m.planChanges() {
				switch i % 4 {
				case 0:
					events = append(events, engine.Event{ID: c.ID, Action: c.Action, Done: true, Duration: time.Second})
				case 1:
					events = append(events, engine.Event{ID: c.ID, Action: c.Action})
				case 2:
					events = append(events, engine.Event{ID: c.ID, Action: c.Action, Done: true, Err: long})
				}
			}
			return applying(t, m, events...)
		},
		"failed": func(t *testing.T, m model) model {
			first := m.planChanges()[0]
			m = applying(t, m, engine.Event{ID: first.ID, Action: first.Action, Done: true, Err: long})
			m, _ = update(t, m, appliedMsg{plan: m.state.plan.plan, applied: true, err: long})
			m.error = ""
			return m
		},
	}
	for _, size := range [][2]int{{78, 22}, {120, 40}, {200, 60}} {
		for _, n := range []int{3, 1500} {
			for name, state := range states {
				t.Run(fmt.Sprintf("%dx%d %d %s", size[0], size[1], n, name), func(t *testing.T) {
					m := pdPlanned(t, pdPlan(n).Changes...)
					m, _ = update(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
					m = state(t, m)
					lines := strings.Split(m.render(), "\n")
					if len(lines) > size[1] {
						t.Errorf("%d lines", len(lines))
					}
					for i, line := range lines {
						if w := ansi.StringWidth(line); w > size[0] {
							t.Errorf("line %d is %d wide: %q", i, w, ansi.Strip(line))
						}
					}
				})
			}
		}
	}
}
