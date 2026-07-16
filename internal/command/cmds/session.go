// Package cmds holds the built-in tmr commands. Related commands are grouped by
// theme in one file (session.go, window.go, server.go) instead of tmux's
// one-file-per-command layout. Importing this package for its side effects
// registers every command.
package cmds

import (
	"github.com/ivpcode/tmr/internal/command"
	"github.com/ivpcode/tmr/internal/tmux"
)

// new-session [-d] [-s name]
var newSession = &command.Command{
	Name:    "new-session",
	Alias:   "new",
	Summary: "create a new session",
	Flags: command.Flags{
		"d": command.Bool("do not attach to the new session"),
		"s": command.Str("session name"),
	},
	MinArgs: 0,
	MaxArgs: 0,
	Run: func(c *command.Ctx) error {
		sess, err := c.Server.NewSession(c.Flag("s"))
		if err != nil {
			return c.Errorf("%v", err)
		}
		// Without -d a real client would attach here; that lands with the
		// rendering layer. For now we acknowledge creation.
		if c.Bool("d") {
			c.Printf("%s\n", sess.Name)
		} else {
			c.Printf("created session %s (attach not yet implemented)\n", sess.Name)
		}
		return nil
	},
}

// kill-session -t session
var killSession = &command.Command{
	Name:    "kill-session",
	Summary: "destroy a session",
	Flags: command.Flags{
		"t": command.Target(tmux.KindSession),
	},
	Run: func(c *command.Ctx) error {
		if c.Target == nil || c.Target.Session == nil {
			return c.Errorf("no target session")
		}
		name := c.Target.Session.Name
		if err := c.Server.KillSession(name); err != nil {
			return c.Errorf("%v", err)
		}
		return nil
	},
}

// list-sessions
var listSessions = &command.Command{
	Name:    "list-sessions",
	Alias:   "ls",
	Summary: "list sessions",
	Run: func(c *command.Ctx) error {
		for _, s := range c.Server.SessionList() {
			c.Printf("%s: %d windows\n", s.Name, len(s.Windows))
		}
		return nil
	},
}

// has-session -t session
var hasSession = &command.Command{
	Name:    "has-session",
	Alias:   "has",
	Summary: "check a session exists",
	Flags: command.Flags{
		"t": command.Target(tmux.KindSession),
	},
	Run: func(c *command.Ctx) error {
		if c.Target == nil || c.Target.Session == nil {
			return c.Errorf("no such session")
		}
		return nil
	},
}

func init() {
	command.Register(newSession, killSession, listSessions, hasSession)
}
