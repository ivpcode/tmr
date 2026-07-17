package command

import (
	"sort"
	"strings"
)

// Usage builds a usage string from the structured Flags spec, so no command has
// to hand-write and maintain its own usage line (tmux keeps a .usage string on
// every cmd_entry that must be updated by hand whenever flags change).
func (cmd *Command) Usage() string {
	var b strings.Builder
	b.WriteString(cmd.Name)

	// Collect flag names for stable, readable ordering: bools first, then
	// value/target flags.
	var bools, values []string
	for name, spec := range cmd.Flags {
		if spec.Type == FlagBool {
			bools = append(bools, name)
		} else {
			values = append(values, name)
		}
	}
	sort.Strings(bools)
	sort.Strings(values)

	if len(bools) > 0 {
		b.WriteString(" [-" + strings.Join(bools, "") + "]")
	}
	for _, name := range values {
		b.WriteString(" [-" + name + " " + argName(cmd.Flags[name]) + "]")
	}
	if cmd.MaxArgs != 0 {
		b.WriteString(" " + argsPlaceholder(cmd))
	}
	return b.String()
}

func argName(f Flag) string {
	switch f.Type {
	case FlagInt:
		return "n"
	default:
		return "value"
	}
}

func argsPlaceholder(cmd *Command) string {
	switch {
	case cmd.MaxArgs == 1 && cmd.MinArgs == 1:
		return "arg"
	case cmd.MaxArgs < 0:
		return "[args...]"
	default:
		return "[args]"
	}
}
