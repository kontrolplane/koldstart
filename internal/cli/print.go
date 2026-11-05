package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

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

// printPlan writes what a plan does, one node a line, with the inputs that change under each
// update.
func printPlan(w io.Writer, s *stage, plan *engine.Plan) {
	printPlanTo(w, s.App, s.Stage, s.Region, s.sess.Account, plan)
}

func printPlanTo(w io.Writer, app, stage, region, account string, plan *engine.Plan) {
	_, _ = fmt.Fprintf(w, "plan for %s/%s in %s, account %s:\n\n", app, stage, region, account)
	if !plan.HasChanges() {
		_, _ = fmt.Fprintln(w, "  no changes")
		return
	}
	for _, c := range plan.Changes {
		if c.Action == engine.NoOp {
			continue
		}
		line := fmt.Sprintf("  %-3s %s", symbols[c.Action], c.ID)
		if c.Resume {
			line += "  (finishing what a deploy started)"
		}
		_, _ = fmt.Fprintln(w, line)
		if c.Action == engine.Update || c.Action == engine.Replace || c.Action == engine.Adopt {
			for _, line := range FieldChanges(c) {
				_, _ = fmt.Fprintf(w, "        %s\n", line)
			}
		}
	}
	counts := plan.Counts()
	var parts []string
	for _, a := range []engine.Action{engine.Create, engine.Update, engine.Replace, engine.Delete, engine.Keep, engine.Adopt} {
		if counts[a] > 0 {
			parts = append(parts, fmt.Sprintf("%d to %s", counts[a], verbs[a]))
		}
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", strings.Join(parts, ", "))
	if plan.OnlyCode() {
		_, _ = fmt.Fprintln(w, "only code changes: the functions' code is replaced in place")
	}
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

// progress returns an event handler that writes a line as each node finishes.
func progress(w io.Writer) func(engine.Event) {
	var mu sync.Mutex
	return func(ev engine.Event) {
		if !ev.Done {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if ev.Err != nil {
			_, _ = fmt.Fprintf(w, "  ✗ %s: %s failed after %s\n", ev.ID, verbs[ev.Action], ev.Duration.Round(100*time.Millisecond))
			return
		}
		_, _ = fmt.Fprintf(w, "  ✓ %s: %s in %s\n", ev.ID, pastTenses[ev.Action], ev.Duration.Round(100*time.Millisecond))
	}
}
