package tui

import (
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// attachment is something a function uses: a resource it links to, by the access its role has on
// it, where lambda sends the events it failed on or handled, the file system it mounts, the
// network it joins and the key its environment is encrypted with. id is the resource in the
// stage, which enter opens, and "" for one outside it.
type attachment struct {
	how, name, kind, copy, id string
}

// The ways a function uses what is not a link.
const (
	howDeadLetter = "dead letter"
	howOnSuccess  = "on success"
	howOnFailure  = "on failure"
	howFileSystem = "file system"
	howVPC        = "vpc"
	howKMSKey     = "kms key"
	howLinked     = "linked"
	howReferenced = "referenced"
)

// attachmentsOf are what fn uses in a stage, as it was deployed: its links, sorted, the other
// resources of the stage its environment refers to, then its dead letter target, its
// destinations, its file system, its vpc and its kms key.
func attachmentsOf(st *state.State, fn string) []attachment {
	return attachmentsFrom(st, fn, desired.Links(st.Resources, fn), arnIndex(st))
}

// arnIndex are the resources of a stage by each of their ids in aws and attributes that is an
// arn, and by their service and the name their id ends with, as a queue's url ends with its name,
// so an arn finds the resource that has it, or one of its service and name where the stage holds
// no arn of it, as on an emulator; where two have one, the first by id.
func arnIndex(st *state.State) map[string]string {
	out := map[string]string{}
	set := func(k, id string) {
		if _, seen := out[k]; !seen {
			out[k] = id
		}
	}
	for _, id := range slices.Sorted(maps.Keys(st.Resources)) {
		r := st.Resources[id]
		for _, v := range slices.Concat([]string{r.PhysicalID}, slices.Collect(maps.Values(r.Attributes))) {
			if strings.HasPrefix(v, "arn:") {
				set(v, id)
			}
		}
		if parts := strings.Split(r.Type, "::"); len(parts) == 3 && r.PhysicalID != "" {
			set(nameKey(strings.ToLower(parts[1]), r.PhysicalID[strings.LastIndexAny(r.PhysicalID, ":/")+1:]), id)
		}
	}
	return out
}

// nameKey is the key of arnIndex for a resource of service by its name.
func nameKey(service, name string) string {
	return "name " + service + " " + name
}

// attachmentsFrom are what fn uses: links, the resources it depends on, and its settings that
// name an arn, found in index.
func attachmentsFrom(st *state.State, fn string, links []desired.Link, index map[string]string) []attachment {
	var out []attachment
	resource := func(how, id string) {
		r := st.Resources[id]
		out = append(out, attachment{how: how, name: headName(id), kind: typeName(r.Type), copy: cmpOr(r.Attributes["Arn"], r.PhysicalID), id: id})
	}
	linked := map[string]bool{}
	for _, l := range links {
		linked[l.ID] = true
		resource(cmpOr(l.Access, howLinked), l.ID)
	}
	f := st.Resources[desired.FunctionID(fn)]
	if f != nil {
		for _, id := range slices.Sorted(slices.Values(f.Dependencies)) {
			if strings.HasPrefix(id, "resource/") && !linked[id] && st.Resources[id] != nil {
				resource(howReferenced, id)
			}
		}
	}
	in := functionInputs(f)
	targets := [][2]string{{howDeadLetter, in.DeadLetter}}
	if in.Async != nil {
		targets = append(targets, [2]string{howOnSuccess, in.Async.OnSuccess}, [2]string{howOnFailure, in.Async.OnFailure})
	}
	if in.FileSystem != nil {
		targets = append(targets, [2]string{howFileSystem, in.FileSystem.AccessPoint})
	}
	for _, t := range targets {
		if t[1] == "" {
			continue
		}
		a := arnAttachment(st, index, t[0], t[1])
		if t[0] == howFileSystem && in.FileSystem.MountPath != "" {
			a.name += " at " + in.FileSystem.MountPath
		}
		out = append(out, a)
	}
	if v := in.VPC; v != nil && len(v.SubnetIDs) > 0 {
		out = append(out, attachment{
			how:  howVPC,
			name: plural(len(v.SubnetIDs), "subnet") + ", " + plural(len(v.SecurityGroupIDs), "security group"),
			kind: "ec2",
			copy: strings.Join(slices.Concat(v.SubnetIDs, v.SecurityGroupIDs), ","),
		})
	}
	if in.KMSKey != "" {
		out = append(out, arnAttachment(st, index, howKMSKey, in.KMSKey))
	}
	return out
}

// arnAttachment is what an arn a function sends to or uses names: the resource of the stage that
// has it in index, or one outside it by the name at its end and its service.
func arnAttachment(st *state.State, index map[string]string, how, arn string) attachment {
	id, ok := index[arn]
	if parts := strings.SplitN(arn, ":", 4); !ok && len(parts) == 4 {
		id, ok = index[nameKey(parts[2], arnName(arn))]
	}
	if ok {
		return attachment{how: how, name: headName(id), kind: typeName(st.Resources[id].Type), copy: arn, id: id}
	}
	return attachment{how: how, name: arnName(arn), kind: arnService(arn), copy: arn}
}

// arnServices are services as a person names them, where that is not their name in an arn.
var arnServices = map[string]string{"elasticfilesystem": "efs", "events": "eventbridge", "secretsmanager": "secrets manager"}

// arnService is the service of an arn, as arn:aws:sqs:… is sqs, or "" for what is not an arn.
func arnService(arn string) string {
	parts := strings.SplitN(arn, ":", 4)
	if len(parts) < 4 || !strings.HasPrefix(arn, "arn:") {
		return ""
	}
	if s, ok := arnServices[parts[2]]; ok {
		return s
	}
	return parts[2]
}

// archRow is a row of the architecture tab: of the function fn, what invokes it and what it uses
// side by side, either nil on a row past the end of its list. i is the row's place in its
// function's rows, of n that invoke it and k it uses.
type archRow struct {
	fn      string
	in      *invoker
	out     *attachment
	i, n, k int
}

// archGroup is a function of the architecture tab with what invokes it and what it uses.
type archGroup struct {
	fn   string
	ins  []invoker
	outs []attachment
}

// archWidths are how wide each part of the architecture tab's rows is, the widest of its rows,
// so the arrows of every function line up.
type archWidths struct {
	in, fn, uses, kind, how, also int
}

// archArrow is how wide the arrows of the architecture tab are.
const archArrow = 4

// architectureRows are the rows of the architecture tab, and the key of each: every function the
// filter keeps, what invokes it, its url first, drawn into it on the left and what it uses out of
// it on the right, on as many rows as the longer of the two. What more than one function uses
// says which others use it. The filter keeps a function by its name or by anything on its rows.
// The tab's one column is titled to the widths of the rows.
func (s *stageState) architectureRows() ([]string, []tableRow) {
	s.arch = nil
	var groups []archGroup
	usedBy := map[string][]string{}
	triggers := desired.TriggersByFunction(s.st.Resources)
	links := desired.LinksByFunction(s.st.Resources)
	index := arnIndex(s.st)
	for _, id := range slices.Sorted(maps.Keys(s.st.Resources)) {
		name, ok := strings.CutPrefix(id, "function/")
		if !ok || s.st.Resources[id].Type != awsp.TypeFunction {
			continue
		}
		g := archGroup{fn: name}
		if u := s.st.Resources[desired.URLID(name)]; u != nil {
			url := u.Attributes["FunctionUrl"]
			g.ins = append(g.ins, invoker{kind: kindURL, what: cmpOr(url, u.PhysicalID), copy: url, id: desired.URLID(name)})
		}
		g.ins = append(g.ins, invokersFrom(s.st, name, triggers[name])...)
		g.outs = attachmentsFrom(s.st, name, links[name], index)
		for _, a := range g.outs {
			if a.id != "" {
				usedBy[a.id] = append(usedBy[a.id], name)
			}
		}
		fields := []string{name}
		for _, t := range g.ins {
			fields = append(fields, t.kind, t.what)
		}
		for _, a := range g.outs {
			fields = append(fields, a.how, a.name, a.kind)
		}
		if s.filter.matches(fields...) {
			groups = append(groups, g)
		}
	}
	var w archWidths
	for _, g := range groups {
		w.fn = max(w.fn, ansi.StringWidth(g.fn))
		for _, t := range g.ins {
			w.in = max(w.in, ansi.StringWidth(styles.Clean(t.kind)+" "+styles.Clean(t.what)))
		}
		for _, a := range g.outs {
			w.uses = max(w.uses, ansi.StringWidth(styles.Clean(a.name)))
			w.kind = max(w.kind, ansi.StringWidth(styles.Clean(a.kind)))
			w.how = max(w.how, ansi.StringWidth(a.how))
			w.also = max(w.also, ansi.StringWidth(alsoNote(sharedWith(usedBy, a.id, g.fn))))
		}
	}
	w.in = max(w.in, len("invoked by"))
	w.fn = max(w.fn, len(functionWord))
	w.uses = max(w.uses, len("uses"))
	w = w.fit(tableWidth() - archInset)
	var keys []string
	var rows []tableRow
	for _, g := range groups {
		for i := range max(1, len(g.ins), len(g.outs)) {
			r := archRow{fn: g.fn, i: i, n: len(g.ins), k: len(g.outs)}
			if i < len(g.ins) {
				r.in = &g.ins[i]
			}
			if i < len(g.outs) {
				r.out = &g.outs[i]
			}
			s.arch = append(s.arch, r)
			keys = append(keys, g.fn+"/"+formatInt(i))
			var others []string
			if r.out != nil {
				others = sharedWith(usedBy, r.out.id, g.fn)
			}
			rows = append(rows, tableRow{archLine(r, w, others)})
		}
	}
	title := padRight("invoked by", w.in+1+archArrow+1) + padRight(functionWord, w.fn+1+archArrow+1) + padRight("uses", w.uses+2)
	if w.kind > 0 {
		title += padRight("type", w.kind+2)
	}
	title += "access"
	s.tables[architectureTab].base = []column{{title: title, width: minColumnWidth, grow: 1}}
	return keys, rows
}

// archInset is the room a row of a table leaves before its first column, for the cursor.
const archInset = 3

// archMin are the narrowest a trigger and what a function uses get before the type is left out.
const (
	archInMin   = 16
	archUsesMin = 12
)

// width is how wide a row drawn to w is.
func (w archWidths) width() int {
	n := w.in + 1 + archArrow + 1 + w.fn + 1 + archArrow + 1 + w.uses + 2 + w.how + w.also
	if w.kind > 0 {
		n += w.kind + 2
	}
	return n
}

// fit narrows w to width: the triggers first, then what is used, and then it leaves out the type.
// What is still too wide the table cuts at its end.
func (w archWidths) fit(width int) archWidths {
	if over := w.width() - width; over > 0 {
		w.in = max(min(w.in, archInMin), w.in-over)
	}
	if over := w.width() - width; over > 0 {
		w.uses = max(min(w.uses, archUsesMin), w.uses-over)
	}
	if w.width() > width {
		w.kind = 0
	}
	return w
}

// sharedWith are the other functions than fn that use the resource id, of usedBy.
func sharedWith(usedBy map[string][]string, id, fn string) []string {
	if id == "" {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(usedBy[id]), func(other string) bool { return other == fn })
}

// alsoNote says which other functions use what a row's function uses, the first by name and how
// many more, as also payments +2, or nothing for none.
func alsoNote(others []string) string {
	switch len(others) {
	case 0:
		return ""
	case 1:
		return "  also " + others[0]
	}
	return "  also " + others[0] + " +" + formatInt(len(others)-1)
}

// cutTo cuts s to width in its middle, as cutMiddle does, or at its end where width is too
// narrow for that.
func cutTo(s string, width int) string {
	if width < 8 {
		return truncate(s, width)
	}
	return cutMiddle(s, width)
}

// padRight pads s with spaces to width.
func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

// archLine draws a row of the architecture tab: what invokes the function, ruled to the arrow
// into it, the function on its first row, ruled to the arrow out of it, then what it uses, its
// type, how and which other functions use it too. A function invoked by nothing has a dash where
// its trigger would be, and one that uses nothing a dash where that would be.
//
//	url https://abc.lambda-url…  ─┬─▶ api ─┬─▶ orders          dynamodb table  readwrite
//	schedule rate(5 minutes) ─────┘        └─▶ orders-dev-dead sqs             dead letter
func archLine(r archRow, w archWidths, others []string) cell {
	rule := func(s string) styles.Span { return styles.S(s, styles.ToneRule) }
	var c cell
	switch {
	case r.in != nil:
		kind := truncate(styles.Clean(r.in.kind), max(1, w.in-2))
		what := cutTo(styles.Clean(r.in.what), max(1, w.in-ansi.StringWidth(kind)-1))
		c = append(c, styles.S(kind+" ", styles.ToneMuted), styles.S(what, styles.ToneBody))
		c = append(c, rule(" "+strings.Repeat("─", max(0, w.in-cellWidth(c)))))
		c = append(c, rule(arrowIn(r.i, r.n)))
	case r.i == 0:
		c = append(c, styles.S(padRight("-", w.in+1+archArrow), styles.ToneFaint))
	default:
		c = append(c, styles.S(strings.Repeat(" ", w.in+1+archArrow), styles.ToneFaint))
	}
	c = append(c, styles.S(" ", styles.ToneFaint))
	switch {
	case r.i == 0 && r.k > 0:
		c = append(c, styles.S(r.fn, styles.ToneText), rule(" "+strings.Repeat("─", w.fn-ansi.StringWidth(r.fn))+arrowOut(r.i, r.k)))
	case r.i == 0:
		c = append(c, styles.S(padRight(r.fn, w.fn+1+archArrow), styles.ToneText))
	default:
		c = append(c, styles.S(strings.Repeat(" ", w.fn+1), styles.ToneFaint), rule(arrowOut(r.i, r.k)))
	}
	c = append(c, styles.S(" ", styles.ToneFaint))
	switch a := r.out; {
	case a != nil:
		c = append(c,
			styles.S(padRight(cutTo(styles.Clean(a.name), w.uses), w.uses+2), styles.ToneBody),
		)
		if w.kind > 0 {
			c = append(c, styles.S(padRight(styles.Clean(a.kind), w.kind+2), styles.ToneMuted))
		}
		c = append(c, styles.S(padRight(a.how, w.how), styles.ToneMuted))
		if len(others) > 0 {
			c = append(c, styles.S(alsoNote(others), styles.ToneFaint))
		}
	case r.i == 0:
		c = append(c, styles.S("-", styles.ToneFaint))
	}
	return c
}

// arrowIn is the arrow from the i-th of n triggers into their function, which is on the first:
// one straight in, or each joined to a line down from the first.
func arrowIn(i, n int) string {
	switch {
	case i >= n:
		return strings.Repeat(" ", archArrow)
	case n == 1:
		return "───▶"
	case i == 0:
		return "─┬─▶"
	case i == n-1:
		return "─┘  "
	}
	return "─┤  "
}

// arrowOut is the arrow from a function, on the first row, out to the i-th of the k things it
// uses: one straight out, or each off a line down from the first.
func arrowOut(i, k int) string {
	switch {
	case i >= k:
		return strings.Repeat(" ", archArrow)
	case k == 1:
		return "───▶"
	case i == 0:
		return "─┬─▶"
	case i == k-1:
		return " └─▶"
	}
	return " ├─▶"
}

// selectedArch is the row of the architecture tab under the cursor, and whether there is one.
func (s stageState) selectedArch() (archRow, bool) {
	i := s.tables[architectureTab].cursor
	if i >= len(s.arch) {
		return archRow{}, false
	}
	return s.arch[i], true
}

// openArch opens what the row under the cursor of the architecture tab names: what the function
// uses, or where the row has none, what invokes it, a url in the browser, or else the function.
func (m model) openArch() (model, tea.Cmd) {
	r, ok := m.state.stage.selectedArch()
	switch {
	case !ok:
		return m, nil
	case r.out != nil && m.inStage(r.out.id):
		return m.openArchResource(r.out.id)
	case r.out == nil && r.in != nil && r.in.kind == kindURL && r.in.copy != "":
		return m.browse(r.in.copy)
	case r.out == nil && r.in != nil && m.inStage(r.in.id):
		return m.openArchResource(r.in.id)
	}
	return m.openFunction(r.fn)
}

// openArchResource opens a resource of the stage, a function on its own page.
func (m model) openArchResource(id string) (model, tea.Cmd) {
	if name, ok := strings.CutPrefix(id, "function/"); ok {
		return m.openFunction(name)
	}
	return m.openResource(stageResource{id: id, res: m.state.stage.st.Resources[id]})
}

// archCopyValue is what c copies on the architecture tab: what the row's function uses, or what
// invokes it, or the function's arn.
func (s stageState) archCopyValue() string {
	r, ok := s.selectedArch()
	switch {
	case !ok:
		return ""
	case r.out != nil && r.out.copy != "":
		return r.out.copy
	case r.out == nil && r.in != nil && r.in.copy != "":
		return r.in.copy
	}
	f := s.st.Resources[desired.FunctionID(r.fn)]
	return cmpOr(f.Attributes["Arn"], f.PhysicalID)
}

// archFunctions counts the functions the architecture tab shows.
func archFunctions(rows []archRow) int {
	n := 0
	for i, r := range rows {
		if i == 0 || rows[i-1].fn != r.fn {
			n++
		}
	}
	return n
}
