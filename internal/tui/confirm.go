package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// unlockState asks to release a stage's lock, saying who took it and when.
type unlockState struct {
	ref      cli.StageRef
	lock     state.LockInfo
	from     page
	selected int
}

// removeState asks to type the stage's name over the plan of its remove.
type removeState struct {
	confirm typedConfirm
}

// runLocalState asks before a function runs here against what prod deployed.
type runLocalState struct {
	event    string
	payload  []byte
	selected int
}

func (m model) askUnlock(ref cli.StageRef, lock *state.LockInfo) (model, tea.Cmd) {
	if lock == nil {
		return m.setStatus(ref.String()+" is not locked", styles.ToneWarning)
	}
	m.state.unlock = unlockState{ref: ref, lock: *lock, from: m.page}
	return m.switchPage(unlockPage), nil
}

func (m model) unlockUpdate(msg tea.Msg) (model, tea.Cmd) {
	u := &m.state.unlock
	switch m.confirmKey(msg, &u.selected) {
	case confirmYes:
		m = m.switchPage(u.from)
		return m, m.unlock(u.ref)
	case confirmNo:
		return m.switchPage(u.from), nil
	}
	return m, nil
}

func (m model) applyUnlocked(msg unlockedMsg) (model, tea.Cmd) {
	if msg.err != nil {
		m.error = "could not release the lock of " + msg.ref.String() + ": " + msg.err.Error()
		return m, nil
	}
	m, status := m.setStatus("released the lock of "+msg.ref.String(), styles.ToneSuccess)
	var load tea.Cmd
	switch m.page {
	case overviewPage:
		m, load = m.loadStages()
	case stagePage:
		m, load = m.loadStage()
	}
	return m, tea.Batch(status, load)
}

func (m model) unlockView() string {
	u := m.state.unlock
	held := []styles.Span{styles.S("held by ", styles.ToneFaint), styles.B(orDash(u.lock.Who), styles.ToneText)}
	if u.lock.Operation != "" {
		held = append(held, styles.S(" for a "+u.lock.Operation, styles.ToneBody))
	}
	if u.lock.Where != "" {
		held = append(held, styles.S(" from "+u.lock.Where, styles.ToneBody))
	}
	since := "since " + formatTime(u.lock.Since) + ", " + formatAgo(u.lock.Since)
	if u.lock.Since.IsZero() {
		since = "since an unknown time"
	}
	return dialog("release the lock of "+u.ref.String()+"?", styles.ToneWarning,
		styles.Render(held...),
		styles.Render(styles.S(since, styles.ToneFaint)),
		"",
		styles.Muted("make sure that deploy is really gone: two deploys of one stage at once can leave its state wrong."),
		"",
		confirmButtons(u.selected, styles.ToneWarning),
	)
}

func (m model) askRemove() (model, tea.Cmd) {
	ref := m.state.plan.ref
	c, cmd := newTypedConfirm(ref.Stage, "remove "+ref.String())
	m.state.remove = removeState{confirm: c}
	m.page = removePage
	return m, cmd
}

func (m model) removeUpdate(msg tea.Msg) (model, tea.Cmd) {
	r := &m.state.remove
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(k, m.keys.Back):
			return m.leavePlan()
		case key.Matches(k, m.keys.View):
			if !r.confirm.ok() {
				r.confirm.tried = true
				return m, nil
			}
			m.page = planPage
			return m.startApply()
		}
	}
	var cmd tea.Cmd
	r.confirm, cmd = r.confirm.update(msg)
	return m, cmd
}

func (m model) removeView() string {
	p := m.state.plan
	counts := p.plan.Counts()
	body := []string{
		styles.Render(styles.S("deletes ", styles.ToneBody), styles.B(plural(counts[engine.Delete], "resource"), styles.ToneDanger),
			styles.S(" of "+p.ref.String()+" in "+p.ref.Region, styles.ToneBody)),
	}
	if n := counts[engine.Keep]; n > 0 {
		hold := " that hold data in place"
		if n == 1 {
			hold = " that holds data in place"
		}
		body = append(body, styles.Muted("and leaves "+plural(n, "resource")+hold))
	}
	body = append(body, "", m.state.remove.confirm.view())
	return dialog("remove "+p.ref.String()+"?", styles.ToneDanger, body...)
}

func (m model) runLocalUpdate(msg tea.Msg) (model, tea.Cmd) {
	r := &m.state.runLocal
	switch m.confirmKey(msg, &r.selected) {
	case confirmYes:
		m = m.switchPage(functionPage)
		return m.invoke(r.event, r.payload, true)
	case confirmNo:
		return m.switchPage(functionPage), nil
	}
	return m, nil
}

func (m model) runLocalView() string {
	return dialog("run "+m.state.function.name+" against prod?", styles.ToneWarning,
		styles.Muted("it runs on this machine with what prod deployed, its data included."),
		"",
		confirmButtons(m.state.runLocal.selected, styles.ToneWarning),
	)
}
