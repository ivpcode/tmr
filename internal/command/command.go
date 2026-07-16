// Package command implements the tmr command system.
//
// This is the deliberately-simplified replacement for tmux's command layer.
// In tmux every command lives in its own cmd-*.c file and declares its
// arguments as a cryptic getopt string (e.g. "af:t:"). Here a command is a
// plain struct with a *structured, readable* flag spec, related commands are
// grouped in one file, and a single dispatcher handles parsing, target
// resolution, usage generation and execution.
package command

import (
	"fmt"
	"io"
	"sort"

	"github.com/ivpcode/tmr/internal/tmux"
)

// FlagType is the kind of value a flag carries.
type FlagType int

const (
	FlagBool   FlagType = iota // -a           (presence only)
	FlagString                 // -s name      (string value)
	FlagInt                    // -n 5         (integer value)
	FlagTarget                 // -t target    (resolves to a session/window/pane)
)

// Flag describes a single option. Instead of tmux's "af:t:" string, each flag
// is declared with a constructor that documents intent:
//
//	Flags{
//	    "a": Bool("kill all but current"),
//	    "s": Str("session name"),
//	    "t": Target(tmux.KindPane),
//	}
type Flag struct {
	Type   FlagType
	Help   string
	Target tmux.TargetKind // only meaningful when Type == FlagTarget
}

// Bool declares a presence-only flag (e.g. -d).
func Bool(help string) Flag { return Flag{Type: FlagBool, Help: help} }

// Str declares a flag that takes a string value (e.g. -s name).
func Str(help string) Flag { return Flag{Type: FlagString, Help: help} }

// Int declares a flag that takes an integer value (e.g. -n 5).
func Int(help string) Flag { return Flag{Type: FlagInt, Help: help} }

// Target declares a -t/-s style flag that names a session, window or pane and
// is resolved automatically before Run is called.
func Target(k tmux.TargetKind) Flag { return Flag{Type: FlagTarget, Target: k} }

// Flags maps a single-letter flag name to its declaration.
type Flags map[string]Flag

// Command is one tmr command. Compare with tmux's struct cmd_entry: same
// concept, but the argument spec is structured data, targets are declarative,
// and Run receives a ready-to-use *Ctx instead of fishing values out of the
// command queue by hand.
type Command struct {
	Name    string
	Alias   string // short alias, e.g. "killp" for "kill-pane" ("" if none)
	Summary string // one line, used by list-commands and usage
	Flags   Flags

	MinArgs int // minimum positional arguments
	MaxArgs int // maximum positional arguments (-1 == unlimited)

	// Run executes the command. Everything it needs — parsed flags, resolved
	// target, the server, and output writers — hangs off Ctx.
	Run func(c *Ctx) error
}

// Ctx carries everything a command needs at execution time. It is the single
// argument to Run, replacing tmux's cmd/cmdq_item pair plus the args_* helpers.
type Ctx struct {
	Cmd    *Command
	Server *tmux.Server
	Target *tmux.Target // resolved from the target flag, nil if none

	bools map[string]bool
	vals  map[string]string
	Args  []string // positional arguments (after flags)

	out io.Writer
	err io.Writer
}

// Bool reports whether a presence flag was given.
func (c *Ctx) Bool(name string) bool { return c.bools[name] }

// Has reports whether a value flag was supplied on the command line.
func (c *Ctx) Has(name string) bool { _, ok := c.vals[name]; return ok }

// Flag returns the string value of a flag ("" if absent).
func (c *Ctx) Flag(name string) string { return c.vals[name] }

// FlagOr returns the string value of a flag, or def if absent.
func (c *Ctx) FlagOr(name, def string) string {
	if v, ok := c.vals[name]; ok {
		return v
	}
	return def
}

// Int returns the integer value of a flag, or def if absent/invalid.
func (c *Ctx) Int(name string, def int) int {
	v, ok := c.vals[name]
	if !ok {
		return def
	}
	n := def
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

// Printf writes normal command output back to the client.
func (c *Ctx) Printf(format string, a ...any) {
	if c.out != nil {
		fmt.Fprintf(c.out, format, a...)
	}
}

// Errorf writes an error message back to the client.
func (c *Ctx) Errorf(format string, a ...any) error {
	msg := fmt.Sprintf(format, a...)
	if c.err != nil {
		fmt.Fprintln(c.err, msg)
	}
	return fmt.Errorf("%s", msg)
}

// ---- Registry -------------------------------------------------------------

var registry = map[string]*Command{} // by name
var aliases = map[string]string{}    // alias -> name
var ordered []*Command               // registration order, for listing

// Register adds one or more commands to the global registry. Commands panic on
// duplicate names so mistakes surface at startup, not at runtime.
func Register(cmds ...*Command) {
	for _, cmd := range cmds {
		if _, dup := registry[cmd.Name]; dup {
			panic("command: duplicate command " + cmd.Name)
		}
		registry[cmd.Name] = cmd
		if cmd.Alias != "" {
			aliases[cmd.Alias] = cmd.Name
		}
		ordered = append(ordered, cmd)
	}
}

// Lookup resolves a command by name or alias.
func Lookup(name string) *Command {
	if c, ok := registry[name]; ok {
		return c
	}
	if real, ok := aliases[name]; ok {
		return registry[real]
	}
	return nil
}

// All returns every registered command sorted by name.
func All() []*Command {
	out := make([]*Command, len(ordered))
	copy(out, ordered)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Dispatch parses argv against the named command and runs it, writing normal
// output to out and errors to errw. argv[0] is the command name/alias.
func Dispatch(srv *tmux.Server, argv []string, out, errw io.Writer) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := Lookup(argv[0])
	if cmd == nil {
		fmt.Fprintf(errw, "unknown command: %s\n", argv[0])
		return fmt.Errorf("unknown command: %s", argv[0])
	}

	ctx, err := cmd.parse(srv, argv[1:], out, errw)
	if err != nil {
		fmt.Fprintf(errw, "%s\nusage: %s\n", err, cmd.Usage())
		return err
	}
	if cmd.Run == nil {
		return ctx.Errorf("%s: not implemented yet", cmd.Name)
	}
	return cmd.Run(ctx)
}
