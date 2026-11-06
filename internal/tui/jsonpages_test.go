package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// crDoc is a document of json longer than a screen, its needle at the end.
func crDoc() string {
	items := make([]map[string]any, 40)
	for i := range items {
		items[i] = map[string]any{"name": "item-" + formatInt(i), "size": i}
	}
	data, _ := json.Marshal(map[string]any{"items": items, "needle": "found"})
	return string(data)
}

// crJSONPages opens each page that reads json in a json view, with crDoc in it: a resource's
// inputs, an output, a create's inputs in a plan, an event on the invoke tab, and a line of the
// logs.
func crJSONPages(t *testing.T) map[string]model {
	t.Helper()
	doc := crDoc()
	var after map[string]any
	if err := json.Unmarshal([]byte(doc), &after); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	st := f.states[ordersDev]
	st.Resources["resource/table"].Inputs = json.RawMessage(doc)
	st.Outputs = map[string]string{"doc": doc}
	f.plan = &engine.Plan{App: "orders", Stage: "dev", Changes: []engine.Change{{ID: "resource/table", Action: engine.Create, After: after}}}
	f.logs = []cli.LogEvent{{Time: time.Now().Add(-time.Minute), Stream: "a", Message: doc}}
	invoke := invokeTabWith(t, f, map[string]string{"doc.json": doc})
	m := pressKeys(t, invoke, "q")
	res := pressKeys(t, m, "tab")
	for range 8 {
		if r, ok := res.state.stage.selectedResource(); ok && r.id == "resource/table" {
			break
		}
		res = pressKeys(t, res, "j")
	}
	return map[string]model{
		"resource": pressKeys(t, res, "enter"),
		"output":   pressKeys(t, m, "tab", "tab", "enter"),
		"inputs":   pressKeys(t, m, "d", "j", "enter"),
		"invoke":   pressKeys(t, invoke, "enter"),
		"log line": pressKeys(t, invoke, "shift+tab", "shift+tab", "enter"),
	}
}

// TestJSONPagesAlike checks every page that reads json does so alike: a search typed or pasted
// takes the cursor to its match in sight, the frame shows the match and the path, n and shift+n
// are in the footer while it holds text, c copies the value under the cursor and shift+c its
// path, saying so, and esc clears the search, then leaves the page or the json.
func TestJSONPagesAlike(t *testing.T) {
	tests := []struct {
		name  string
		paste bool
	}{
		{name: "typed"},
		{name: "pasted", paste: true},
	}
	for _, tt := range tests {
		for page, m := range crJSONPages(t) {
			t.Run(tt.name+" "+page, func(t *testing.T) {
				if m.state.json(m.page) == nil {
					t.Fatalf("no json view on the %s page %d:\n%s", page, m.page, screen(m))
				}
				m = pressKeys(t, m, "/")
				if tt.paste {
					m, _ = update(t, m, tea.PasteMsg{Content: "needle"})
				} else {
					m = typeText(t, m, "needle")
				}
				m = pressKeys(t, m, "enter")
				v := screen(m)
				cursor := ""
				for line := range strings.SplitSeq(v, "\n") {
					if strings.Contains(line, "▌") && strings.Contains(line, `"needle"`) {
						cursor = line
					}
				}
				if cursor == "" {
					t.Errorf("the match is not under the cursor in sight:\n%s", v)
				}
				for _, want := range []string{"search needle · 1 of 1", ".needle", "n/shift+n next / previous"} {
					if !strings.Contains(v, want) {
						t.Errorf("no %q:\n%s", want, v)
					}
				}
				if got := pressKeys(t, m, "c"); got.status != "copied the value of .needle" {
					t.Errorf("c says %q", got.status)
				}
				if got := pressKeys(t, m, "C"); got.status != "copied the path .needle" {
					t.Errorf("shift+c says %q", got.status)
				}
				cleared := pressKeys(t, m, "esc")
				if j := cleared.state.json(cleared.page); cleared.page != m.page || j == nil || j.query != "" {
					t.Errorf("esc did not clear the search first: page %d", cleared.page)
				}
				if back := pressKeys(t, cleared, "esc"); back.state.json(back.page) != nil && back.page == m.page && back.state.function.pane.focused {
					t.Errorf("esc after the search kept the keys in the json")
				}
			})
		}
	}
}
