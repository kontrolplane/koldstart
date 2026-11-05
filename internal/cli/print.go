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
			line += "  " + colour.paint(sgrFaint, "(finishing what a deploy started)")
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
		_, _ = fmt.Fprintln(w, "only code changes: the functions' code is replaced in place")
	}
}

// planCounts counts the changes of a plan, as 2 to create, 1 to update.
func planCounts(plan *engine.Plan) string {
	counts := plan.Counts()
	var parts []string
	for _, a := range actionOrder {
		if counts[a] > 0 {
			parts = append(parts, fmt.Sprintf("%d to %s", counts[a], verbs[a]))
		}
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// FieldChanges says how each changed input changes. The properties of a resource are listed one
// by one, as they are what the configuration sets.
func FieldChanges(c engine.Change) []string {
	var out []string
	for _, f := range c.Fields {
		before, isMapB := c.Before[f].(map[string]any)
		after, isMapA := c.After[f].(map[string]any)
		if f == engine.SecretChanged {
			out = append(out, "a secret or another stage's output changed")
			continue
		}
		if f != "properties" || !isMapB || !isMapA {
			out = append(out, f+": "+describeChange(c.Before[f], c.After[f]))
			continue
		}
		for _, k := range engine.ChangedKeys(before, after) {
			out = append(out, f+"."+k+": "+describeChange(before[k], after[k]))
		}
	}
	return out
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
