package cmds

import "github.com/ivpcode/tmr/internal/command"

// kill-server
var killServer = &command.Command{
	Name:    "kill-server",
	Summary: "stop the tmr server",
	Run: func(c *command.Ctx) error {
		c.Printf("server shutting down\n")
		// Defer the actual shutdown until after this reply is flushed; the
		// server closes the listener, existing connections still drain.
		defer command.Shutdown()
		return nil
	},
}

// list-commands
var listCommands = &command.Command{
	Name:    "list-commands",
	Alias:   "lscm",
	Summary: "list all commands",
	Run: func(c *command.Ctx) error {
		for _, cmd := range command.All() {
			alias := ""
			if cmd.Alias != "" {
				alias = " (" + cmd.Alias + ")"
			}
			c.Printf("%-16s%-10s %s\n", cmd.Name, alias, cmd.Usage())
		}
		return nil
	},
}

func init() {
	command.Register(killServer, listCommands)
}
