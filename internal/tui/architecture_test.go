package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
)

// deadLetterARN is the dead letter target of arFake's api, a queue outside the stage.
const deadLetterARN = "arn:aws:sqs:eu-west-1:123456789012:orders-dev-dead"

// arFake is the fake with an api that a url and a schedule invoke, that links to the table with
// a policy that reads and writes it, and sends what failed to a queue outside the stage, and a
// worker that nothing invokes and that uses nothing.
func arFake(t *testing.T) *fakeBackend {
	t.Helper()
	f := newFake(t)
	st := f.states[ordersDev]
	fn := st.Resources["function/api"]
	var in map[string]any
	if err := json.Unmarshal(fn.Inputs, &in); err != nil {
		t.Fatal(err)
	}
	env := in["environment"].(map[string]any)
	env["TABLE_NAME"], env["TABLE_ARN"] = "orders-dev", "arn:aws:dynamodb:table"
	in["deadLetter"] = deadLetterARN
	fn.Inputs, _ = json.Marshal(in)
	st.Resources["role/api"].Inputs, _ = json.Marshal(map[string]any{
		"name": "orders-dev-api",
		"policy": map[string]any{"Version": "2012-10-17", "Statement": []any{
			map[string]any{"Effect": "Allow", "Action": []any{"dynamodb:GetItem", "dynamodb:PutItem"}, "Resource": []any{"arn:aws:dynamodb:table", "arn:aws:dynamodb:table/index/*"}},
		}},
	})
	st.Resources["url/api"] = &state.Resource{PhysicalID: "u", Attributes: map[string]string{"FunctionUrl": "https://abc.lambda-url.eu-west-1.on.aws/"}}
	st.Resources["schedule/api/0"] = &state.Resource{PhysicalID: "s", Inputs: json.RawMessage(`{"expression":"rate(5 minutes)"}`)}
	worker, _ := json.Marshal(map[string]any{"name": "orders-dev-worker", "runtime": "provided.al2023", "memory": 128, "timeout": 3})
	st.Resources["function/worker"] = &state.Resource{Type: awsp.TypeFunction, PhysicalID: "orders-dev-worker", Inputs: worker,
		Attributes: map[string]string{"Arn": "arn:aws:lambda:eu-west-1:123456789012:function:orders-dev-worker"}}
	return f
}

// arTab is the interface on the architecture tab of orders/dev.
func arTab(t *testing.T, f *fakeBackend) model {
	t.Helper()
	return pressKeys(t, started(t, f), "4")
}

// TestArchitectureRows checks each function is drawn between what invokes it, its url first,
// with arrows into it, and what it uses, with arrows out of it, on as many rows as the longer of
// the two: its links with the access its role has, its dead letter target by the name its arn
// ends with, a dash where a side has nothing, and which other functions use the same resource.
func TestArchitectureRows(t *testing.T) {
	f := arFake(t)
	st := f.states[ordersDev]
	worker, _ := json.Marshal(map[string]any{"name": "orders-dev-worker", "environment": map[string]any{"TABLE_NAME": "orders-dev", "TABLE_ARN": "arn:aws:dynamodb:table"}})
	st.Resources["function/worker"].Inputs = worker
	m := arTab(t, f)
	if m.state.stage.tab != architectureTab {
		t.Fatalf("4 opened tab %d", m.state.stage.tab)
	}
	want := []string{
		"url https:/…da-url.eu-west-1.on.aws/ ─┬─▶ api ──────┬─▶ table            dynamodb table  readwrite    also worker",
		"schedule rate(5 minutes) ─────────────┘             └─▶ orders-dev-dead  sqs             dead letter",
		"-                                         worker ─────▶ table            dynamodb table  linked       also api",
	}
	rows := m.state.stage.tables[architectureTab].rows
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if got := strings.TrimRight(plainText(rows[i][0]), " "); got != w {
			t.Errorf("row %d =\n%q, want\n%q", i, got, w)
		}
	}
	if v := screen(m); !strings.Contains(v, "4 architecture 2") {
		t.Errorf("the tab bar does not count 2 functions:\n%s", v)
	}
}

// TestArchitectureFits checks a row of the tab fits the narrowest terminal, its arrows still in
// line from row to row, and leaves out the type to do so.
func TestArchitectureFits(t *testing.T) {
	m := arTab(t, arFake(t))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 78, Height: 22})
	rows := m.state.stage.tables[architectureTab].rows
	first, second := plainText(rows[0][0]), plainText(rows[1][0])
	if w := ansi.StringWidth(first); w > tableWidth()-archInset {
		t.Errorf("a row is %d wide, past %d: %q", w, tableWidth()-archInset, first)
	}
	if a, b := strings.Index(first, "┬"), strings.Index(second, "┘"); a < 0 || a != b {
		t.Errorf("the arrows into api are not in line:\n%s\n%s", first, second)
	}
	if strings.Contains(first, "dynamodb table") {
		t.Errorf("the type was not left out: %q", first)
	}
}

// TestArchitectureKeys checks enter and c on each row: what the function uses when it is of the
// stage, the function where it is not, the url in the browser, and what each copies.
func TestArchitectureKeys(t *testing.T) {
	tests := []struct {
		name     string
		down     int
		page     page
		resource string
		function string
		copied   string
	}{
		{name: "a link", down: 0, page: resourcePage, resource: "resource/table", copied: "arn:aws:dynamodb:table"},
		{name: "outside the stage", down: 1, page: functionPage, function: "api", copied: deadLetterARN},
		{name: "nothing on either side", down: 2, page: functionPage, function: "worker", copied: "arn:aws:lambda:eu-west-1:123456789012:function:orders-dev-worker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := arTab(t, arFake(t))
			for range tt.down {
				m = pressKeys(t, m, "j")
			}
			if got := m.stageCopyValue(); got != tt.copied {
				t.Errorf("c copies %q, want %q", got, tt.copied)
			}
			m = pressKeys(t, m, "enter")
			if m.page != tt.page {
				t.Fatalf("enter opened page %d, want %d", m.page, tt.page)
			}
			if tt.resource != "" && m.state.resource.res.id != tt.resource {
				t.Errorf("enter opened %s, want %s", m.state.resource.res.id, tt.resource)
			}
			if tt.function != "" && m.state.function.name != tt.function {
				t.Errorf("enter opened %s, want %s", m.state.function.name, tt.function)
			}
		})
	}
}

// TestArchitectureFilter checks the filter keeps a function by what is on its rows, as the
// target of its dead letter, and says so when it keeps none.
func TestArchitectureFilter(t *testing.T) {
	m := pressKeys(t, arTab(t, arFake(t)), "/")
	m = pressKeys(t, typeText(t, m, "dead"), "enter")
	if n := archFunctions(m.state.stage.arch); n != 1 || m.state.stage.arch[0].fn != "api" {
		t.Errorf("the filter kept %d functions: %v", n, m.state.stage.arch)
	}
	m = pressKeys(t, m, "esc", "/")
	m = pressKeys(t, typeText(t, m, "nothing-like-this"), "enter")
	if v := screen(m); !strings.Contains(v, "no function matches nothing-like-this") {
		t.Errorf("the empty tab:\n%s", v)
	}
}

// TestUsesSection checks the function's overview lists what it uses, its link by its access and
// its dead letter target, and that a function that uses nothing has no section.
func TestUsesSection(t *testing.T) {
	m := pressKeys(t, started(t, arFake(t)), "enter")
	v := screen(m)
	for _, want := range []string{"uses 2", "readwrite  table  dynamodb table", "dead letter  orders-dev-dead  sqs"} {
		if !strings.Contains(v, want) {
			t.Errorf("the overview lacks %q:\n%s", want, v)
		}
	}
	m = pressKeys(t, started(t, arFake(t)), "j", "enter")
	if v := screen(m); m.state.function.name != "worker" || strings.Contains(v, "uses") {
		t.Errorf("the worker's overview has a uses section:\n%s", v)
	}
}

// TestTabRowFits checks the tabs of a stage leave out their counts rather than run past the
// width the narrowest terminal gives them.
func TestTabRowFits(t *testing.T) {
	counts := []string{"12", "1,204", "18", "30", "12"}
	for _, width := range []int{74, 120} {
		bar, _, _ := strings.Cut(tabRow(stageTabs, counts, 0, width), "\n")
		if w := ansi.StringWidth(bar); w > width {
			t.Errorf("at %d the tabs are %d wide: %q", width, w, ansi.Strip(bar))
		}
	}
	if bar := ansi.Strip(tabRow(stageTabs, counts, 0, 120)); !strings.Contains(bar, "2 resources 1,204") {
		t.Errorf("at 120 the counts were left out: %q", bar)
	}
}

// TestReferenced checks a resource the function's environment refers to, by kdst.ref, is used,
// as referenced, after its links and not again when it is linked too.
func TestReferenced(t *testing.T) {
	f := arFake(t)
	st := f.states[ordersDev]
	st.Resources["resource/carts"] = &state.Resource{Type: "AWS::DynamoDB::Table", PhysicalID: "carts-dev"}
	st.Resources["function/api"].Dependencies = []string{"loggroup/api", "resource/table", "resource/carts", "role/api"}
	uses := attachmentsOf(st, "api")
	got := make([]string, 0, len(uses))
	for _, a := range uses {
		got = append(got, a.how+" "+a.name)
	}
	want := []string{"readwrite table", "referenced carts", "dead letter orders-dev-dead"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("api uses %v, want %v", got, want)
	}
}

// TestArnAttachment checks an arn a function uses finds the resource of the stage that has it, or
// where the stage holds no arn of it, as floci keeps none of a queue, the one of its service whose
// id ends with its name; and one outside the stage by the end of its arn.
func TestArnAttachment(t *testing.T) {
	st := state.New("checkout", "development", "eu-west-1", "000000000000")
	st.Resources["resource/table"] = &state.Resource{Type: "AWS::DynamoDB::Table", PhysicalID: "orders", Attributes: map[string]string{"Arn": "arn:aws:dynamodb:eu-west-1:000000000000:table/orders"}}
	st.Resources["resource/failed"] = &state.Resource{Type: "AWS::SQS::Queue", PhysicalID: "http://localhost:4566/000000000000/checkout-development-failed"}
	tests := []struct {
		name string
		arn  string
		id   string
		kind string
	}{
		{name: "by its arn", arn: "arn:aws:dynamodb:eu-west-1:000000000000:table/orders", id: "resource/table", kind: "dynamodb table"},
		{name: "by its name", arn: "arn:aws:sqs:eu-west-1:000000000000:checkout-development-failed", id: "resource/failed", kind: "sqs queue"},
		{name: "another service of the name", arn: "arn:aws:sns:eu-west-1:000000000000:checkout-development-failed", kind: "sns"},
		{name: "outside the stage", arn: "arn:aws:sqs:eu-west-1:000000000000:elsewhere", kind: "sqs"},
	}
	index := arnIndex(st)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := arnAttachment(st, index, howDeadLetter, tt.arn)
			if a.id != tt.id || a.kind != tt.kind || a.copy != tt.arn {
				t.Errorf("arnAttachment(%s) = %+v, want id %q kind %q", tt.arn, a, tt.id, tt.kind)
			}
		})
	}
}
