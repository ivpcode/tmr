// Package cmds holds the built-in ivt commands that run on the server: they
// change session state and return text. The interactive commands (resume, to)
// live in the client, since they take over the terminal.
package cmds

import (
	"fmt"
	"strings"
	"time"

	"github.com/ivpcode/tmr/internal/command"
)

// new [name] [command [args...]]
//
// Creates a session. With no name one is generated. With a command, the session
// runs it; otherwise it runs the login shell. The session name is printed so
// the client can attach to it.
var newSession = &command.Command{
	Name:    "new",
	Alias:   "n",
	Summary: "create a session (optionally running a command)",
	MinArgs: 0,
	MaxArgs: -1,
	Run: func(c *command.Ctx) error {
		name := ""
		var cmd []string
		if len(c.Args) >= 1 {
			name = c.Args[0]
		}
		if len(c.Args) >= 2 {
			cmd = c.Args[1:]
		}
		cols, rows := c.Cols, c.Rows
		if cols == 0 {
			cols, rows = 80, 24
		}
		sess, err := c.Server.NewSession(name, cmd, cols, rows)
		if err != nil {
			return c.Errorf("%v", err)
		}
		c.Printf("%s\n", sess.Name()) // just the name: the client reads it to attach
		return nil
	},
}

// ls — list sessions
var lsSessions = &command.Command{
	Name:    "ls",
	Summary: "list sessions",
	Run: func(c *command.Ctx) error {
		for _, s := range c.Server.List() {
			mark := ""
			if s.Attached > 0 {
				mark = " (attached)"
			}
			c.Printf("%s: %s  [%s]%s\n",
				s.Name, cmdline(s.Cmd), age(s.Created), mark)
		}
		return nil
	},
}

// kill <name> — destroy a session
var killSession = &command.Command{
	Name:    "kill",
	Summary: "destroy a session",
	MinArgs: 1,
	MaxArgs: 1,
	Run: func(c *command.Ctx) error {
		if err := c.Server.Kill(c.Args[0]); err != nil {
			return c.Errorf("%v", err)
		}
		return nil
	},
}

// rename <old> <new> — rename a session
var renameSession = &command.Command{
	Name:    "rename",
	Alias:   "rn",
	Summary: "rename a session",
	MinArgs: 2,
	MaxArgs: 2,
	Run: func(c *command.Ctx) error {
		if err := c.Server.Rename(c.Args[0], c.Args[1]); err != nil {
			return c.Errorf("%v", err)
		}
		return nil
	},
}

// detach <name> — detach clients from a session
var detachSession = &command.Command{
	Name:    "detach",
	Alias:   "d",
	Summary: "detach clients from a session",
	MinArgs: 1,
	MaxArgs: 1,
	Run: func(c *command.Ctx) error {
		if err := c.Server.DetachClients(c.Args[0]); err != nil {
			return c.Errorf("%v", err)
		}
		return nil
	},
}

// to <name> — switch the active client to another session
var toSession = &command.Command{
	Name:    "to",
	Summary: "switch the active client to another session",
	MinArgs: 1,
	MaxArgs: 1,
	Run: func(c *command.Ctx) error {
		if err := c.Server.SwitchActive(c.Args[0]); err != nil {
			return c.Errorf("%v", err)
		}
		return nil
	},
}

func init() {
	command.Register(newSession, lsSessions, killSession, renameSession, detachSession, toSession)
}

// cmdline renders a session's command for listing.
func cmdline(cmd []string) string {
	if len(cmd) == 0 {
		return "(shell)"
	}
	return strings.Join(cmd, " ")
}

// age renders a short human duration since t.
func age(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
