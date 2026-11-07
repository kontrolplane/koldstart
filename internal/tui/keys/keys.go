// Package keys holds the keybindings of the terminal interface.
package keys

import "charm.land/bubbles/v2/key"

// KeyMap holds the keybindings shared by the views.
type KeyMap struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Top      key.Binding
	Bottom   key.Binding
	Left     key.Binding
	Right    key.Binding
	Help     key.Binding
	View     key.Binding
	Filter   key.Binding
	NextTab  key.Binding
	PrevTab  key.Binding
	// Tab opens a page's tab by its number, as the tab row numbers them.
	Tab       key.Binding
	Deploy    key.Binding
	Apply     key.Binding
	Remove    key.Binding
	Unlock    key.Binding
	Invoke    key.Binding
	RunLocal  key.Binding
	EditEvent key.Binding
	Follow    key.Binding
	// Level, Group and Stamps change how the logs are shown: the least level a line is shown
	// at, its lines grouped by request, and the column of times.
	Level  key.Binding
	Group  key.Binding
	Stamps key.Binding
	// Request opens the invocation a line of the logs belongs to, a page with no plan to group, so
	// it shares o with GroupBy.
	Request  key.Binding
	Fold     key.Binding
	Copy     key.Binding
	NextDiff key.Binding
	PrevDiff key.Binding
	Unfold   key.Binding
	GroupBy  key.Binding
	// Pane switches the invoke tab between an event's payload and what it returned.
	Pane key.Binding
	// Next and Previous step through the invocations from one of them, or the lines of the logs
	// from one, pages with no dialog to answer and nothing to pause, so they share n and p with No
	// and Pause.
	Next      key.Binding
	Previous  key.Binding
	CopyPath  key.Binding
	NextMatch key.Binding
	PrevMatch key.Binding
	Refresh   key.Binding
	Pause     key.Binding
	Yes       key.Binding
	No        key.Binding
	Back      key.Binding
	Quit      key.Binding
	ForceQuit key.Binding
}

// Keys are the keybindings docs/ui.md lists. A help key names every key of its binding, as the
// footer and the key reference write them: arrows as ↑ ↓ ← →, alternatives joined with /.
var Keys = KeyMap{
	Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	PageUp:    key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
	PageDown:  key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
	Top:       key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g/home", "top")),
	Bottom:    key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G/end", "bottom")),
	Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "left")),
	Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
	Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	View:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
	NextTab:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
	PrevTab:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous tab")),
	Tab:       key.NewBinding(key.WithKeys("1", "2", "3", "4"), key.WithHelp("1-4", "open tab")),
	Deploy:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "plan")),
	Apply:     key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "apply")),
	Remove:    key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "remove")),
	Unlock:    key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "unlock")),
	Invoke:    key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "invoke")),
	RunLocal:  key.NewBinding(key.WithKeys("I", "shift+i"), key.WithHelp("shift+i", "run here")),
	EditEvent: key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit event")),
	Follow:    key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "follow")),
	Level:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "level")),
	Group:     key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "by request")),
	Stamps:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "time")),
	Request:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "its invocation")),
	Fold:      key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "fold")),
	Copy:      key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy")),
	NextDiff:  key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next change")),
	PrevDiff:  key.NewBinding(key.WithKeys("["), key.WithHelp("[", "previous change")),
	Unfold:    key.NewBinding(key.WithKeys("z"), key.WithHelp("z", "unfold all")),
	GroupBy:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "group by")),
	Pane:      key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "payload / result")),
	Next:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next")),
	Previous:  key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "previous")),
	CopyPath:  key.NewBinding(key.WithKeys("C", "shift+c"), key.WithHelp("shift+c", "copy path")),
	NextMatch: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next match")),
	PrevMatch: key.NewBinding(key.WithKeys("N", "shift+n"), key.WithHelp("shift+n", "previous match")),
	Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Pause:     key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause")),
	Yes:       key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yes")),
	No:        key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "no")),
	Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "back")),
	ForceQuit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}
