package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// planFacts is what the plan page works out once for a plan: the changes that do something, in
// the plan's order, what is said under each, and their groups by kind and by action.
type planFacts struct {
	plan     *engine.Plan
	changes  []engine.Change
	details  []changeDetail
	byKind   []planGroup
	byAction []planGroup
}

// changeDetail is what the plan page says under a change: the fields it changes, as the text
// plan lists them, and of a replace why it is one and what happens to the old resource.
type changeDetail struct {
	fields []cli.FieldChange
	reason string
	loss   string
	// lost is a replace that deletes what the old resource holds.
	lost bool
}

// planGroup is a group of the changes of a plan: of a kind of node, or of an action.
type planGroup struct {
	// key is what the group is folded by.
	key  string
	name string
	// action is the action of a group by action, and counts the actions of a group by kind.
	action  engine.Action
	counts  []cli.ActionCount
	members []int
}

// planRow is a row of the plan the cursor can be on: the heading of a group, or a change, by its
// index among planFacts.changes, -1 on a heading.
type planRow struct {
	group  int
	change int
}

// planKinds are the kinds of node the changes are grouped in by kind, in their order: functions,
// what each brings, its role, log group, url, permissions and events, the http api with its
// routes, domain and authorizers, the resources, and anything else, named as the stage's tree
// names its kinds.
var planKinds = []string{kindNames[functionGroups], "roles, logs and triggers", kindNames[httpGroup], kindNames[resourceGroups], kindNames[otherGroups]}

// planKind is the kind of node id is, by its index in planKinds.
func planKind(id string) int {
	if strings.HasPrefix(id, desired.FunctionID("")) {
		return 0
	}
	switch kind, _ := desired.Group(id, nil); kind {
	case desired.GroupFunction:
		return 1
	case desired.GroupHTTP:
		return 2
	case desired.GroupResource:
		return 3
	}
	return 4
}

// hasFields reports whether a change of the action lists the fields it changes.
func hasFields(a engine.Action) bool {
	return a == engine.Update || a == engine.Replace || a == engine.Adopt
}

// forcedLast orders the fields of a change: those that force a replace first, 0, and the rest
// after, 1, so the fields a replace is for are among the ones listed.
func forcedLast(c engine.Change, f cli.FieldChange) int {
	if c.Action == engine.Replace && slices.Contains(c.Forces, f.Field) {
		return 0
	}
	return 1
}

// newPlanFacts works out the changes of plan that do something, what each changes, and their
// groups by kind and by action, each group in the plan's order.
func newPlanFacts(plan *engine.Plan) *planFacts {
	f := &planFacts{plan: plan}
	if plan == nil {
		return f
	}
	for _, c := range plan.Changes {
		if c.Action == engine.NoOp {
			continue
		}
		d := changeDetail{reason: cli.ReplaceReason(c)}
		d.loss, d.lost = cli.ReplaceLoss(c)
		if hasFields(c.Action) {
			d.fields = cli.FieldDetails(c)
			slices.SortStableFunc(d.fields, func(a, b cli.FieldChange) int {
				return cmp.Compare(forcedLast(c, a), forcedLast(c, b))
			})
		}
		f.changes = append(f.changes, c)
		f.details = append(f.details, d)
	}
	kinds := make([][]int, len(planKinds))
	actions := map[engine.Action][]int{}
	for i, c := range f.changes {
		k := planKind(c.ID)
		kinds[k] = append(kinds[k], i)
		actions[c.Action] = append(actions[c.Action], i)
	}
	for k, members := range kinds {
		if len(members) == 0 {
			continue
		}
		sub := &engine.Plan{}
		for _, i := range members {
			sub.Changes = append(sub.Changes, engine.Change{Action: f.changes[i].Action})
		}
		f.byKind = append(f.byKind, planGroup{key: "kind/" + planKinds[k], name: planKinds[k], counts: cli.PlanCounts(sub), members: members})
	}
	for _, c := range cli.PlanCounts(plan) {
		f.byAction = append(f.byAction, planGroup{key: "action/" + string(c.Action), name: cli.Verb(c.Action), action: c.Action, members: actions[c.Action]})
	}
	return f
}

// planRows are the rows of the plan on screen: the heading of each group, and the changes of
// each group not folded.
func planRows(p planState) (*planFacts, []planGroup, []planRow) {
	f := p.facts()
	groups := f.byKind
	if p.byAction {
		groups = f.byAction
	}
	rows := make([]planRow, 0, len(f.changes)+len(groups))
	for g, group := range groups {
		rows = append(rows, planRow{group: g, change: -1})
		if p.folded[group.key] {
			continue
		}
		for _, c := range group.members {
			rows = append(rows, planRow{group: g, change: c})
		}
	}
	return f, groups, rows
}

// firstChangeRow is the row of the first change in sight, or the first row.
func firstChangeRow(p planState) int {
	_, _, rows := planRows(p)
	for i, r := range rows {
		if r.change >= 0 {
			return i
		}
	}
	return 0
}

// planMaxFields is how many fields are listed under a change before the rest are counted.
const planMaxFields = 3

// planDetailIndent is the columns the lines under a change are indented by, past its symbol.
const planDetailIndent = 8

// planLines are the lines of the plan: the head, which stays in place, and the body of groups
// and changes, each of which can be chosen. A group's heading counts its changes, and while
// applying how many are done; a change lists the fields it changes, of a replace why it is one
// and what it loses, and the error it failed with.
func (m model) planLines() ([]string, pickList) {
	p := m.state.plan
	width := contentWidth - 5
	if p.plan == nil {
		return nil, pickList{}
	}
	head := m.planHead(width)
	f, groups, rows := planRows(p)
	var body pickList
	if len(f.changes) == 0 {
		body.lines.add(p.noChanges(width)...)
		return head, body
	}
	cols := []column{{width: width - planStatusWidth - 1}, {width: 1}, {width: planStatusWidth, right: true}}
	pad := strings.Repeat(" ", planDetailIndent)
	room := column{width: width - planDetailIndent}
	for _, r := range rows {
		if r.change < 0 {
			g := groups[r.group]
			if r.group > 0 {
				body.lines.add("")
			}
			folded := p.folded[g.key]
			body.addDrawn(func() string {
				return renderCell(groupCell(g, folded), cols[0], nil) + " " + renderCell(m.groupProgress(g, f), cols[2], nil)
			}, func() string { return litRow(cols, groupCell(g, folded), nil, m.groupProgress(g, f)) })
			continue
		}
		c, d := f.changes[r.change], f.details[r.change]
		body.addDrawn(func() string {
			return renderCell(changeCell(c), cols[0], nil) + " " + renderCell(m.progressCell(c), cols[2], nil)
		}, func() string { return litRow(cols, changeCell(c), nil, m.progressCell(c)) })
		var notes []cell
		if len(c.Drift) > 0 {
			notes = append(notes, text("▲ changed outside koldstart: "+strings.Join(c.Drift, ", "), styles.ToneWarning))
		}
		if d.reason != "" {
			notes = append(notes, text("▲ "+d.reason, styles.ToneWarning))
		}
		switch {
		case d.lost:
			notes = append(notes, text("✗ "+d.loss, styles.ToneDanger))
		case d.loss != "":
			notes = append(notes, text(d.loss, styles.ToneInfo))
		}
		if len(notes) > 0 {
			body.lines.addDrawn(len(notes), func(i int) string { return pad + renderCell(notes[i], room, nil) })
		}
		fields := d.fields
		shown := min(len(fields), planMaxFields)
		body.lines.addDrawn(shown, func(i int) string { return pad + fieldLine(fields[i], room.width) })
		if more := len(fields) - shown; more > 0 {
			body.lines.addDrawn(1, func(int) string {
				return pad + renderCell(text("+"+formatInt(more)+" more · enter for the diff", styles.ToneFaint), room, nil)
			})
		}
		if pr := p.progress[c.ID]; pr.err != nil {
			for _, l := range errorLines(c.ID, pr.err, width-planDetailIndent) {
				body.lines.add(pad + l)
			}
		}
	}
	return head, body
}

// changeCell is the line of a change of the plan: its action and its id, the type of a resource,
// and what is special about it.
func changeCell(c engine.Change) cell {
	line := cell{
		styles.S(fmt.Sprintf("  %-4s", cli.Symbol(c.Action)), actionTones[c.Action]),
		styles.S(c.ID, styles.ToneText),
	}
	if strings.HasPrefix(c.Type, "AWS::") {
		line = append(line, styles.S("  "+typeName(c.Type), styles.ToneMuted))
	}
	switch {
	case c.Resume:
		line = append(line, styles.S("  "+cli.ResumeNote(c.Action), styles.ToneFaint))
	case c.Gone:
		line = append(line, styles.S("  deleted outside koldstart", styles.ToneWarning))
	}
	return line
}

// groupCell is the heading of a group: ▾ over its changes, or ▸ when it is folded, its name, how
// many changes it holds, and in a group by kind how many of each action.
func groupCell(g planGroup, folded bool) cell {
	marker := "▾ "
	if folded {
		marker = "▸ "
	}
	c := cell{styles.S(marker, styles.ToneFaint)}
	if g.action != "" {
		c = append(c, styles.S(cli.Symbol(g.action)+" ", actionTones[g.action]))
	}
	c = append(c, styles.B(g.name, styles.ToneText), styles.S("  "+formatInt(len(g.members)), styles.ToneFaint))
	for _, n := range g.counts {
		c = append(c, styles.S("  "+cli.Symbol(n.Action)+formatInt(n.N), actionTones[n.Action]))
	}
	return c
}

// groupProgress says how far the changes of a group got while applying: how many of them are
// done, as 2/5, ✓ once all are, and how many failed.
func (m model) groupProgress(g planGroup, f *planFacts) cell {
	p := m.state.plan
	if p.progress == nil {
		return nil
	}
	done, failed := 0, 0
	for _, i := range g.members {
		pr, ok := p.progress[f.changes[i].ID]
		switch {
		case !ok && !p.applying && !p.failed:
			done++
		case ok && pr.err != nil:
			failed++
		case ok && pr.done:
			done++
		}
	}
	count := formatInt(done) + "/" + formatInt(len(g.members))
	switch {
	case failed > 0:
		return cell{styles.S("✗ "+formatInt(failed)+" failed", styles.ToneDanger), styles.S(" · "+count, styles.ToneFaint)}
	case done == len(g.members):
		return cell{styles.S("✓ ", styles.ToneSuccess), styles.S(count, styles.ToneBody)}
	}
	return cell{styles.S(count, styles.ToneBody), styles.S(" done", styles.ToneFaint)}
}

// markSpan is a span of a line of the plan, marked when it is the part of a value that changed.
type markSpan struct {
	styles.Span
	mark bool
}

// fieldLine is a field a change changes, in width: its name, and its value before and after
// with the part that changed marked, as memory 512 → 1024, or how it changes, as environment.KEY
// added, and of a function's code that it changed, its hashes faint.
func fieldLine(f cli.FieldChange, width int) string {
	name := styles.S(f.Field+" ", styles.ToneMuted)
	var spans []markSpan
	switch {
	case f.Field == engine.SecretChanged:
		spans = []markSpan{{Span: styles.S(f.String(), styles.ToneInfo)}}
	case f.Code:
		spans = []markSpan{{Span: name}, {Span: styles.S("changed", styles.ToneBody)}, {Span: styles.S("  "+f.Before+" → "+f.After, styles.ToneFaint)}}
	case f.How != "":
		tone := styles.ToneBody
		switch f.How {
		case cli.HowAdded:
			tone = styles.ToneSuccess
		case cli.HowRemoved:
			tone = styles.ToneDanger
		case cli.HowSecret:
			tone = styles.ToneInfo
		}
		spans = []markSpan{{Span: name}, {Span: styles.S(f.How, tone)}}
	default:
		before, after := styles.Clean(f.Before), styles.Clean(f.After)
		mb, ma := markChanged(before, after)
		spans = append([]markSpan{{Span: name}}, markedSpans(before, mb, styles.ToneDanger)...)
		spans = append(spans, markSpan{Span: styles.S(" → ", styles.ToneFaint)})
		spans = append(spans, markedSpans(after, ma, styles.ToneSuccess)...)
	}
	return drawMarked(spans, width)
}

// markedSpans cuts s, a value, into spans of body text, and of the byte ranges marks in the tone
// marked.
func markedSpans(s string, marks [][2]int, marked styles.Tone) []markSpan {
	var out []markSpan
	at := 0
	for _, r := range marks {
		if r[0] > at {
			out = append(out, markSpan{Span: styles.Span{Text: s[at:r[0]], Tone: styles.ToneBody}})
		}
		out = append(out, markSpan{Span: styles.Span{Text: s[r[0]:r[1]], Tone: marked}, mark: true})
		at = r[1]
	}
	if at < len(s) {
		out = append(out, markSpan{Span: styles.Span{Text: s[at:], Tone: styles.ToneBody}})
	}
	return out
}

// drawMarked draws spans in width, the marked ones as the diff marks what changed, ending in an
// ellipsis when they do not fit. Only what is drawn is measured.
func drawMarked(spans []markSpan, width int) string {
	fit := func(room int) ([]markSpan, bool) {
		out := make([]markSpan, 0, len(spans))
		used := 0
		for _, s := range spans {
			txt, w, whole := fitText(s.Text, room-used)
			if txt != "" {
				s.Text = txt
				out = append(out, s)
			}
			used += w
			if !whole {
				return out, false
			}
		}
		return out, true
	}
	parts, whole := fit(width)
	if !whole {
		parts, _ = fit(width - 1)
	}
	var b strings.Builder
	for _, s := range parts {
		if s.mark {
			b.WriteString(markStyle(s.Tone).Render(s.Text))
			continue
		}
		b.WriteString(s.Style().Render(s.Text))
	}
	if !whole {
		b.WriteString(styles.Faint("…"))
	}
	return b.String()
}
