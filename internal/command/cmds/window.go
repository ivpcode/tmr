package cmds

import (
	"github.com/ivpcode/tmr/internal/command"
	"github.com/ivpcode/tmr/internal/tmux"
)

// list-windows [-t session]
var listWindows = &command.Command{
	Name:    "list-windows",
	Alias:   "lsw",
	Summary: "list windows in a session",
	Flags: command.Flags{
		"t": command.Target(tmux.KindSession),
	},
	Run: func(c *command.Ctx) error {
		if c.Target == nil || c.Target.Session == nil {
			return c.Errorf("no target session")
		}
		for _, w := range c.Target.Session.Windows {
			active := ""
			if w.Index == c.Target.Session.CurWin {
				active = " (active)"
			}
			c.Printf("%d: %s [%d panes]%s\n", w.Index, w.Name, len(w.Panes), active)
		}
		return nil
	},
}

// list-panes [-t window]
var listPanes = &command.Command{
	Name:    "list-panes",
	Alias:   "lsp",
	Summary: "list panes in a window",
	Flags: command.Flags{
		"t": command.Target(tmux.KindWindow),
	},
	Run: func(c *command.Ctx) error {
		if c.Target == nil || c.Target.Window == nil {
			return c.Errorf("no target window")
		}
		for i, p := range c.Target.Window.Panes {
			c.Printf("%d: [%dx%d] id=%d\n", i, p.Width, p.Height, p.ID)
		}
		return nil
	},
}

func init() {
	command.Register(listWindows, listPanes)
}
