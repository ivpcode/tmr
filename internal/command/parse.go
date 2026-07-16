package command

import (
	"fmt"
	"io"

	"github.com/ivpcode/tmr/internal/tmux"
)

// parse turns a raw argument slice into a fully-populated *Ctx: it splits flags
// from positional arguments, validates them against the command's Flags spec,
// enforces MinArgs/MaxArgs, and resolves the target flag into c.Target.
//
// This is the hand-written replacement for getopt + args_parse. It supports the
// two forms tmux uses: -abc (combined booleans) and -s value / -svalue.
func (cmd *Command) parse(srv *tmux.Server, argv []string, out, errw io.Writer) (*Ctx, error) {
	c := &Ctx{
		Cmd:    cmd,
		Server: srv,
		bools:  map[string]bool{},
		vals:   map[string]string{},
		out:    out,
		err:    errw,
	}

	i := 0
	for i < len(argv) {
		arg := argv[i]
		// "--" ends flag parsing; "-" alone is a positional argument.
		if arg == "--" {
			i++
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			break // first non-flag: positional args start here
		}

		// Walk the letters in this token (handles -abc and -svalue).
		j := 1
		for j < len(arg) {
			name := string(arg[j])
			spec, ok := cmd.Flags[name]
			if !ok {
				return nil, fmt.Errorf("unknown flag -%s", name)
			}
			if spec.Type == FlagBool {
				c.bools[name] = true
				j++
				continue
			}
			// Value flag: take the rest of this token, or the next token.
			var val string
			if j+1 < len(arg) {
				val = arg[j+1:]
			} else {
				i++
				if i >= len(argv) {
					return nil, fmt.Errorf("flag -%s requires a value", name)
				}
				val = argv[i]
			}
			c.vals[name] = val
			j = len(arg) // consumed the rest of the token
		}
		i++
	}

	c.Args = argv[i:]

	// Enforce positional-argument bounds.
	if len(c.Args) < cmd.MinArgs {
		return nil, fmt.Errorf("need at least %d argument(s), got %d", cmd.MinArgs, len(c.Args))
	}
	if cmd.MaxArgs >= 0 && len(c.Args) > cmd.MaxArgs {
		return nil, fmt.Errorf("too many arguments (max %d)", cmd.MaxArgs)
	}

	if err := c.resolveTarget(); err != nil {
		return nil, err
	}
	return c, nil
}

// resolveTarget finds the single Target flag (if any) and resolves its value
// against the server, filling c.Target. When the flag is absent it falls back
// to the current/most-recent object of the requested kind.
func (c *Ctx) resolveTarget() error {
	for name, spec := range c.Cmd.Flags {
		if spec.Type != FlagTarget {
			continue
		}
		val, given := c.vals[name]
		t, err := c.Server.ResolveTarget(spec.Target, val, given)
		if err != nil {
			return err
		}
		c.Target = t
		return nil // at most one target flag per command
	}
	return nil
}
