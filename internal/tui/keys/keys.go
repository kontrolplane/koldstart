// Package keys holds the keybindings of the terminal interface.
package keys

import "charm.land/bubbles/v2/key"

// KeyMap holds the keybindings shared by the views.
type KeyMap struct {
	Up        key.Binding
	Down      key.Binding
	PageUp    key.Binding
	PageDown  key.Binding
	Top       key.Binding
	Bottom    key.Binding
	Left      key.Binding
	Right     key.Binding
	Help      key.Binding
	View      key.Binding
	Filter    key.Binding
	NextTab   key.Binding
	PrevTab   key.Binding
	Deploy    key.Binding
	Apply     key.Binding
	Remove    key.Binding
	Unlock    key.Binding
	Invoke    key.Binding
	RunLocal  key.Binding
	EditEvent key.Binding
	Follow    key.Binding
	Copy      key.Binding
	Refresh   key.Binding
	Pause     key.Binding
	Yes       key.Binding
	No        key.Binding
	Back      key.Binding
	Quit      key.Binding
	ForceQuit key.Binding
}

// Keys are the keybindings docs/ui.md lists.
var Keys = KeyMap{
	Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑ | k", "up")),
	Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓ | j", "down")),
	PageUp:    key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
	PageDown:  key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
	Top:       key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
	Bottom:    key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
	Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("← | h", "left")),
	Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→ | l", "right")),
	Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	View:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
	NextTab:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
	PrevTab:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous tab")),
	Deploy:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "plan")),
	Apply:     key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "apply")),
	Remove:    key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "remove")),
	Unlock:    key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "unlock")),
	Invoke:    key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "invoke")),
	RunLocal:  key.NewBinding(key.WithKeys("I", "shift+i"), key.WithHelp("shift+i", "run here")),
	EditEvent: key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit event")),
	Follow:    key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "follow")),
	Copy:      key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy")),
	Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Pause:     key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause")),
	Yes:       key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yes")),
	No:        key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "no")),
	Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "back")),
	ForceQuit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}
