package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// symbols mark each action in a plan.
var symbols = map[engine.Action]string{
	engine.Create:  "+",
	engine.Update:  "~",
	engine.Replace: "-/+",
	engine.Delete:  "-",
	engine.Keep:    "=",
	engine.Adopt:   "<",
}

// verbs say what each action does, for the summary and progress.
var verbs = map[engine.Action]string{
	engine.Create:  "create",
	engine.Update:  "update",
	engine.Replace: "replace",
	engine.Delete:  "delete",
	engine.Keep:    "leave in place",
	engine.Adopt:   "take over again",
}

// Symbol marks an action in a plan: + to create, ~ to update, -/+ to replace, - to delete.
func Symbol(a engine.Action) string { return symbols[a] }

// Verb says what an action does, as create or leave in place.
func Verb(a engine.Action) string { return verbs[a] }

// PastTense says what an action did, as created or left in place.
func PastTense(a engine.Action) string { return pastTenses[a] }

// Participle says what an action is doing, as creating or leaving in place.
func Participle(a engine.Action) string { return participles[a] }

// ResumeNote says what a change does with a node a deploy left unfinished: finishes it, or, as
// it deletes or leaves it, only that it was left so.
func ResumeNote(a engine.Action) string {
	if a == engine.Delete || a == engine.Keep {
		return "a deploy left it unfinished"
	}
	return "finishing what a deploy started"
}

// ActionCount is how many changes of a plan take an action.
type ActionCount struct {
	Action engine.Action
	N      int
}

// String says what the changes do, as 2 to create.
func (c ActionCount) String() string { return fmt.Sprintf("%d to %s", c.N, verbs[c.Action]) }

// PlanCounts counts the changes of a plan by action, in the order its summary names them, and
// leaves out the actions it takes none of.
func PlanCounts(plan *engine.Plan) []ActionCount {
	counts := plan.Counts()
	var out []ActionCount
	for _, a := range actionOrder {
		if counts[a] > 0 {
			out = append(out, ActionCount{Action: a, N: counts[a]})
		}
	}
	return out
}

// ApplySummary counts what applying a plan did, as 8 created, 1 updated, with how many failed
// and how many were stopped, and after either how many never started, or says nothing changed.
func ApplySummary(done map[engine.Action]int, failed, stopped, notStarted int) string {
	var parts []string
	for _, a := range actionOrder {
		if done[a] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", done[a], pastTenses[a]))
		}
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if stopped > 0 {
		parts = append(parts, fmt.Sprintf("%d stopped", stopped))
	}
	if notStarted > 0 {
		parts = append(parts, fmt.Sprintf("%d not started", notStarted))
	}
	if len(parts) == 0 {
		return "nothing changed"
	}
	return strings.Join(parts, ", ")
}

// actionOrder is the order the summaries of a plan and of applying it count actions in.
var actionOrder = []engine.Action{engine.Create, engine.Update, engine.Replace, engine.Delete, engine.Keep, engine.Adopt}

// tones colour the symbol of each action in a plan.
var tones = map[engine.Action]string{
	engine.Create:  sgrGreen,
	engine.Update:  sgrYellow,
	engine.Replace: sgrRed,
	engine.Delete:  sgrRed,
	engine.Keep:    sgrFaint,
	engine.Adopt:   sgrCyan,
}

// maxIDWidth is the widest the column of ids is padded to; a longer id pushes the rest of its
// own line along.
const maxIDWidth = 40

// printPlan writes what a plan does, one node a line, with the inputs that change under each
// update.
func printPlan(w io.Writer, s *stage, plan *engine.Plan) {
	printPlanTo(w, s.App, s.Stage, s.Region, s.sess.Account, plan)
}

// printPlanTo writes a plan as printPlan does: the symbol of each action, the node, the type of a
// resource, and a summary that counts them.
func printPlanTo(w io.Writer, app, stage, region, account string, plan *engine.Plan) {
	colour := paletteFor(w)
	_, _ = fmt.Fprintf(w, "plan for %s/%s in %s, account %s\n\n", app, stage, region, account)
	if !plan.HasChanges() {
		if plan.Refreshed {
			_, _ = fmt.Fprintln(w, "  no changes: what is in aws matches the configuration")
			return
		}
		_, _ = fmt.Fprintln(w, "  no changes: what the state says is deployed matches the configuration")
		return
	}
	width := 0
	for _, c := range plan.Changes {
		if c.Action != engine.NoOp {
			width = max(width, min(len(c.ID), maxIDWidth))
		}
	}
	for _, c := range plan.Changes {
		if c.Action == engine.NoOp {
			continue
		}
		line := "  " + colour.paint(tones[c.Action], padRight(symbols[c.Action], 3)) + " " + padRight(c.ID, width)
		if strings.HasPrefix(c.Type, "AWS::") {
			line += "  " + colour.paint(sgrFaint, c.Type)
		}
		switch {
		case c.Resume:
			line += "  " + colour.paint(sgrFaint, "("+ResumeNote(c.Action)+")")
		case c.Gone:
			line += "  " + colour.paint(sgrFaint, "(deleted outside koldstart)")
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))
		if len(c.Drift) > 0 {
			_, _ = fmt.Fprintf(w, "        changed outside koldstart: %s\n", strings.Join(c.Drift, ", "))
		}
		if c.Action == engine.Update || c.Action == engine.Replace || c.Action == engine.Adopt {
			for _, change := range FieldChanges(c) {
				_, _ = fmt.Fprintf(w, "        %s\n", change)
			}
		}
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", planCounts(plan))
	if plan.OnlyCode() {
		whose := "the functions'"
		if counts := plan.Counts(); counts[engine.Update] == 1 {
			whose = "the function's"
		}
		_, _ = fmt.Fprintf(w, "only code changes: %s code is replaced in place\n", whose)
	}
}

// planCounts counts the changes of a plan, as 2 to create, 1 to update.
func planCounts(plan *engine.Plan) string {
	counts := PlanCounts(plan)
	if len(counts) == 0 {
		return "no changes"
	}
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		parts = append(parts, c.String())
	}
	return strings.Join(parts, ", ")
}

// FieldChanges says how each changed input changes. The properties of a resource are listed one
// by one, as they are what the configuration sets.
func FieldChanges(c engine.Change) []string {
	var out []string
	for _, d := range fieldDiffs(c) {
		if d.field == engine.SecretChanged {
			out = append(out, "a secret or another stage's output changed")
			continue
		}
		out = append(out, d.field+": "+describeChange(d.before, d.after))
	}
	return out
}

// fieldDiff is one input a change changes, from before to after.
type fieldDiff struct {
	field         string
	before, after any
}

// fieldDiffs are the inputs a change changes, the properties of a resource one by one, as
// FieldChanges and plan -o json list them. engine.SecretChanged stands for a secret or another
// stage's output that changed.
func fieldDiffs(c engine.Change) []fieldDiff {
	var out []fieldDiff
	for _, f := range c.Fields {
		before, isMapB := c.Before[f].(map[string]any)
		after, isMapA := c.After[f].(map[string]any)
		if f == engine.SecretChanged || f != "properties" || !isMapB || !isMapA {
			out = append(out, fieldDiff{field: f, before: c.Before[f], after: c.After[f]})
			continue
		}
		for _, k := range engine.ChangedKeys(before, after) {
			out = append(out, fieldDiff{field: f + "." + k, before: before[k], after: after[k]})
		}
	}
	return out
}

// planJSON is a plan as plan -o json prints it.
type planJSON struct {
	App       string         `json:"app"`
	Stage     string         `json:"stage"`
	Region    string         `json:"region"`
	Account   string         `json:"account"`
	Refreshed bool           `json:"refreshed"`
	Changes   []changeJSON   `json:"changes"`
	Counts    map[string]int `json:"counts"`
}

// changeJSON is one change of a plan -o json: what the text plan says of it.
type changeJSON struct {
	Action string `json:"action"`
	ID     string `json:"id"`
	Type   string `json:"type"`
	// Fields are the inputs an update, replace or take over changes, and SecretChanged says only
	// a secret or another stage's output did.
	Fields        []fieldJSON `json:"fields,omitempty"`
	SecretChanged bool        `json:"secretChanged,omitempty"`
	Drift         []string    `json:"drift,omitempty"`
	Resume        bool        `json:"resume,omitempty"`
	Gone          bool        `json:"gone,omitempty"`
}

// fieldJSON is one input a change changes. Before is null for one added, and after for one
// removed.
type fieldJSON struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// writePlanJSON writes a plan as one json object for a script, with the changes the text plan
// lists, in its order.
func writePlanJSON(w io.Writer, app, stage, region, account string, plan *engine.Plan) error {
	out := planJSON{App: app, Stage: stage, Region: region, Account: account, Refreshed: plan.Refreshed, Changes: []changeJSON{}, Counts: map[string]int{}}
	for _, c := range plan.Changes {
		if c.Action == engine.NoOp {
			continue
		}
		out.Counts[string(c.Action)]++
		change := changeJSON{Action: string(c.Action), ID: c.ID, Type: c.Type, Drift: c.Drift, Resume: c.Resume, Gone: c.Gone}
		if c.Action == engine.Update || c.Action == engine.Replace || c.Action == engine.Adopt {
			for _, d := range fieldDiffs(c) {
				if d.field == engine.SecretChanged {
					change.SecretChanged = true
					continue
				}
				change.Fields = append(change.Fields, fieldJSON{Field: d.field, Before: shownInput(d.before), After: shownInput(d.after)})
			}
		}
		out.Changes = append(out.Changes, change)
	}
	return writeJSON(w, out)
}

// ownMarker is the key the engine marks its own values with inside inputs: a secret, another
// stage's output, or a value known only after apply.
const ownMarker = "$koldstart"

// shownInput is an input as plan -o json shows it: koldstart's own values by what they stand
// for, as engine.Describe writes them, such as ssm:/orders/api-key for a secret, whose value
// is never in a plan.
func shownInput(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if _, own := x[ownMarker]; own {
			return engine.Describe(x)
		}
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = shownInput(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = shownInput(item)
		}
		return out
	}
	return v
}

// describeChange says how an input changes: before and after for short values, less for the rest.
func describeChange(before, after any) string {
	switch {
	case engine.IsUnknown(after):
		return "known after apply"
	case engine.IsSecret(after):
		return "(a secret)"
	}
	if b, a := codeHash(before), codeHash(after); b != "" && a != "" {
		return b + " → " + a
	}
	b, a := short(before), short(after)
	if b != "" && a != "" {
		return b + " → " + a
	}
	if before == nil {
		return "added"
	}
	if after == nil {
		return "removed"
	}
	return "changed"
}

// codeHash returns the start of the sha256 of a function's code input, or "".
func codeHash(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	sha, _ := m["sha256"].(string)
	if len(sha) < 12 {
		return ""
	}
	return sha[:12]
}

// short returns a value as one short line, or "" when it does not fit one.
func short(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		if len(x) > 60 {
			return ""
		}
		return fmt.Sprintf("%q", x)
	case float64, bool:
		return fmt.Sprint(x)
	case map[string]any:
		if len(x) > 4 {
			return ""
		}
		data, _ := json.Marshal(x)
		if len(data) > 60 {
			return ""
		}
		return string(data)
	default:
		return ""
	}
}

// pastTenses say what each action did, for progress.
var pastTenses = map[engine.Action]string{
	engine.Create:  "created",
	engine.Update:  "updated",
	engine.Replace: "replaced",
	engine.Delete:  "deleted",
	engine.Keep:    "left in place",
	engine.Adopt:   "taken over",
}

// The sgr codes of the colours the cli writes in: the terminal's own sixteen, so they suit its
// theme.
const (
	sgrFaint  = "2"
	sgrRed    = "31"
	sgrGreen  = "32"
	sgrYellow = "33"
	sgrCyan   = "36"
)

// palette colours text when on, and leaves it as it is otherwise.
type palette struct {
	on bool
}

// paletteFor colours what goes to w when w is a terminal, unless NO_COLOR is set or TERM is
// dumb.
func paletteFor(w io.Writer) palette {
	f, ok := w.(*os.File)
	return palette{on: ok && colourWanted(isTerminal(f), os.Getenv)}
}

// colourWanted reports whether output to a terminal, or not, is coloured with the environment
// getenv reads.
func colourWanted(terminal bool, getenv func(string) string) bool {
	return terminal && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

// paint colours s with the sgr code.
func (p palette) paint(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
