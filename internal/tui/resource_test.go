package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

// openTable opens resource/table of orders/dev from the resources tab.
func openTable(t *testing.T, f *fakeBackend) model {
	t.Helper()
	m := pressKeys(t, started(t, f), "tab", "j", "j", "j", "j", "enter")
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
			want: []string{"✗ could not read it from aws", "aws said no", "an emulator may not have"},
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

// TestResourceDrift checks a property changed outside koldstart is shown with what it was
// applied with and what aws has now.
func TestResourceDrift(t *testing.T) {
	f := newFake(t)
	f.states[ordersDev].Resources["resource/table"].Inputs = []byte(`{"properties":{"BillingMode":"PAY_PER_REQUEST","KeySchema":[{"AttributeName":"pk"}]}}`)
	f.live = map[string]any{"BillingMode": "PROVISIONED", "KeySchema": []any{map[string]any{"AttributeName": "id"}}}
	v := screen(pressKeys(t, openTable(t, f), "tab"))
	for _, want := range []string{
		"changed outside koldstart: BillingMode, KeySchema",
		`BillingMode  "PAY_PER_REQUEST"  →  "PROVISIONED"`,
		`KeySchema  [{"AttributeName":"pk"}]  →  [{"AttributeName":"id"}]`,
		"the next deploy sets them back",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("no %q in:\n%s", want, v)
		}
	}
}

// TestDriftedLines checks every line of a drifted property is marked, its nested value included,
// and none of the others.
func TestDriftedLines(t *testing.T) {
	lines := jsonLines(map[string]any{
		"Arn":       "arn",
		"KeySchema": []any{map[string]any{"AttributeName": "id"}},
		"Tags":      map[string]any{"team": "orders"},
		"Name":      "x",
	})
	got := driftedLines(lines, map[string]bool{"KeySchema": true, "Name": true})
	var marked []string
	for i, line := range lines {
		if got[i] {
			marked = append(marked, strings.TrimSpace(line))
		}
	}
	want := []string{`"KeySchema": [`, "{", `"AttributeName": "id"`, "}", "],", `"Name": "x",`}
	if strings.Join(marked, "\n") != strings.Join(want, "\n") {
		t.Errorf("marked:\n%s\nwant:\n%s", strings.Join(marked, "\n"), strings.Join(want, "\n"))
	}
}

// TestResourceScroll checks the resource page stops scrolling at its last line, so ↑ moves at
// once after G.
func TestResourceScroll(t *testing.T) {
	f := newFake(t)
	f.live = map[string]any{}
	for i := range 80 {
		f.live[strings.Repeat("k", i+1)] = i
	}
	m := pressKeys(t, openTable(t, f), "tab", "G")
	bottom := m.state.resource.scroll
	if bottom == 0 || bottom > len(m.resourceLines()) {
		t.Fatalf("G: scroll %d of %d lines", bottom, len(m.resourceLines()))
	}
	if m = pressKeys(t, m, "k"); m.state.resource.scroll != bottom-1 {
		t.Errorf("k after G: scroll %d, want %d", m.state.resource.scroll, bottom-1)
	}
}

// TestResourceCopy checks c copies the id in aws, or the arn of a resource that has no id.
func TestResourceCopy(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "id", id: "orders-dev", want: "copied orders-dev"},
		{name: "arn", want: "copied arn:aws:dynamodb:table"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.states[ordersDev].Resources["resource/table"].PhysicalID = tt.id
			if m := pressKeys(t, openTable(t, f), "c"); m.status != tt.want {
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
