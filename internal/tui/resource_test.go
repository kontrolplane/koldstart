package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

// openTable opens resource/table of orders/dev from the resources tab.
func openTable(t *testing.T, f *fakeBackend) model {
	t.Helper()
	m := pressKeys(t, started(t, f), "tab", "G", "enter")
	if m.page != resourcePage || m.state.resource.res.id != "resource/table" {
		t.Fatalf("page %v on %s, want resource/table", m.page, m.state.resource.res.id)
	}
	return m
}

// TestResourceLiveStates checks what the in aws tab says for each way reading a resource ends:
// left in place, not created yet, a type cloud control cannot read, an error, and nothing read.
func TestResourceLiveStates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fakeBackend)
		msg   *liveLoadedMsg
		want  []string
	}{
		{
			name:  "not created yet",
			setup: func(f *fakeBackend) { f.states[ordersDev].Resources["resource/table"].PhysicalID = "" },
			want:  []string{"it has no id in aws yet: the next deploy creates it"},
		},
		{
			name: "a type cloud control cannot read",
			want: []string{"cloud control cannot read a AWS::DynamoDB::Table"},
		},
		{
			name: "an emulator without cloud control",
			msg:  &liveLoadedMsg{err: errAWS},
			want: []string{"could not read it from aws: aws said no · r tries again", "an emulator may not have"},
		},
		{
			name: "no properties",
			msg:  &liveLoadedMsg{props: map[string]any{}},
			want: []string{"aws returned no properties for it"},
		},
		{
			name: "as applied",
			msg:  &liveLoadedMsg{props: map[string]any{"BillingMode": "PAY_PER_REQUEST"}},
			want: []string{"✓ as koldstart applied it", `"BillingMode": "PAY_PER_REQUEST"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			m := pressKeys(t, openTable(t, f), "tab")
			if tt.msg != nil {
				msg := *tt.msg
				msg.gen = m.state.resource.liveGen
				m, _ = update(t, m, msg)
			}
			v := screen(m)
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("no %q in:\n%s", want, v)
				}
			}
			if strings.Contains(v, "reading it from aws") {
				t.Errorf("the spinner stays:\n%s", v)
			}
		})
	}
}

// TestResourceDrift checks a property changed outside koldstart is named above the properties,
// and marked ▲ on each of its lines in them, its first as what it was applied with → what aws has
// now.
func TestResourceDrift(t *testing.T) {
	f := newFake(t)
	f.states[ordersDev].Resources["resource/table"].Inputs = []byte(`{"properties":{"BillingMode":"PAY_PER_REQUEST","KeySchema":[{"AttributeName":"pk"}],"Name":"t"}}`)
	f.live = map[string]any{"BillingMode": "PROVISIONED", "KeySchema": []any{map[string]any{"AttributeName": "id"}}, "Name": "t"}
	v := screen(pressKeys(t, openTable(t, f), "tab"))
	for _, want := range []string{
		"changed outside koldstart: BillingMode, KeySchema",
		`▲   "BillingMode": "PAY_PER_REQUEST" → "PROVISIONED",`,
		`▲   "KeySchema": [{"AttributeName":"pk"}] → [`,
		`▲       "AttributeName": "id"`,
		`  "Name": "t"`,
		"the next deploy sets them back",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("no %q in:\n%s", want, v)
		}
	}
	if strings.Contains(v, `▲ "Name"`) {
		t.Errorf("a property as applied is marked:\n%s", v)
	}
}

// TestResourceLongAttributes checks an attribute named as cloud control names them, longer than
// the labels koldstart writes, shows whole, as VisibilityTimeout of a queue was cut.
func TestResourceLongAttributes(t *testing.T) {
	f := newFake(t)
	f.states[ordersDev].Resources["resource/table"].Attributes = map[string]string{
		"VisibilityTimeout": "60", "AttributeDefinitions": "id", "Arn": "arn:aws:dynamodb:table",
	}
	v := screen(openTable(t, f))
	for _, want := range []string{"VisibilityTimeout  60", "AttributeDefinitions  id", "Arn  arn:aws:dynamodb:table"} {
		if !strings.Contains(v, want) {
			t.Errorf("the attributes do not show %q:\n%s", want, v)
		}
	}
}

// TestResourceCopy checks c copies the value under the cursor of the json and shift+c its path,
// and, on a tab without json, the id in aws, or the arn of a resource that has no id.
func TestResourceCopy(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		inputs string
		keys   []string
		want   string
	}{
		{name: "all of it", id: "orders-dev", keys: []string{"c"}, want: "copied all of it"},
		{name: "a value", id: "orders-dev", keys: []string{"j", "j", "c"}, want: "copied the value of .properties.BillingMode"},
		{name: "a path", id: "orders-dev", keys: []string{"j", "j", "C"}, want: "copied the path .properties.BillingMode"},
		{name: "the id without json", id: "orders-dev", inputs: "null", keys: []string{"c"}, want: "copied orders-dev"},
		{name: "the arn without json or id", inputs: "null", keys: []string{"c"}, want: "copied arn:aws:dynamodb:table"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			table := f.states[ordersDev].Resources["resource/table"]
			table.PhysicalID = tt.id
			if tt.inputs != "" {
				table.Inputs = []byte(tt.inputs)
			}
			if m := pressKeys(t, openTable(t, f), tt.keys...); m.status != tt.want {
				t.Errorf("status %q, want %q", m.status, tt.want)
			}
		})
	}
}

// TestResourceRefresh checks r reads the resource again, showing it is on its way.
func TestResourceRefresh(t *testing.T) {
	f := newFake(t)
	f.live = map[string]any{"BillingMode": "PAY_PER_REQUEST"}
	m := pressKeys(t, openTable(t, f), "tab")
	f.live = map[string]any{"BillingMode": "PROVISIONED"}
	m, cmd := update(t, m, press("r"))
	if v := screen(m); !strings.Contains(v, "reading it from aws") {
		t.Errorf("r does not show the read:\n%s", v)
	}
	if v := screen(settle(t, m, cmd)); !strings.Contains(v, `"PROVISIONED"`) {
		t.Errorf("r does not show what aws has now:\n%s", v)
	}
}

// TestResourceEscapes checks the keys of a resource's attributes and properties never bring an
// escape sequence to the terminal.
func TestResourceEscapes(t *testing.T) {
	evil := "x\x1b]8;;https://evil.example/\x07y\x1b[2J"
	f := newFake(t)
	table := f.states[ordersDev].Resources["resource/table"]
	table.Attributes[evil] = evil
	inputs, err := json.Marshal(map[string]any{"properties": map[string]any{evil: "applied"}})
	if err != nil {
		t.Fatal(err)
	}
	table.Inputs = inputs
	f.live = map[string]any{evil: evil}
	m := openTable(t, f)
	live := pressKeys(t, m, "tab")
	if !strings.Contains(screen(live), "changed outside koldstart") {
		t.Fatalf("the property is not drifted:\n%s", screen(live))
	}
	for _, page := range []model{m, live} {
		if v := page.render(); strings.Contains(v, "\x1b]") || strings.Contains(v, "\x07") || strings.Contains(v, "\x1b[2J") {
			t.Errorf("tab %v: an escape sequence reached the view", page.state.resource.tab)
		}
	}
}
