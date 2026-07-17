package command

import (
	"bytes"
	"testing"

	"github.com/ivpcode/tmr/internal/tmux"
)

// testCmd exercises every flag type the parser supports.
var testCmd = &Command{
	Name: "test",
	Flags: Flags{
		"a": Bool("bool a"),
		"b": Bool("bool b"),
		"s": Str("string s"),
		"n": Int("int n"),
	},
	MinArgs: 0,
	MaxArgs: -1,
}

func parseArgs(t *testing.T, argv []string) *Ctx {
	t.Helper()
	srv := tmux.NewServer(nil)
	c, err := testCmd.parse(srv, argv, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse(%v): %v", argv, err)
	}
	return c
}

func TestParseBoolsAndValues(t *testing.T) {
	c := parseArgs(t, []string{"-a", "-s", "hello", "-n", "42", "pos1", "pos2"})
	if !c.Bool("a") {
		t.Error("-a should be set")
	}
	if c.Bool("b") {
		t.Error("-b should not be set")
	}
	if c.Flag("s") != "hello" {
		t.Errorf("-s = %q, want hello", c.Flag("s"))
	}
	if c.Int("n", 0) != 42 {
		t.Errorf("-n = %d, want 42", c.Int("n", 0))
	}
	if len(c.Args) != 2 || c.Args[0] != "pos1" || c.Args[1] != "pos2" {
		t.Errorf("positional args = %v", c.Args)
	}
}

func TestParseCombinedBools(t *testing.T) {
	c := parseArgs(t, []string{"-ab"})
	if !c.Bool("a") || !c.Bool("b") {
		t.Errorf("combined -ab not both set: a=%v b=%v", c.Bool("a"), c.Bool("b"))
	}
}

func TestParseAttachedValue(t *testing.T) {
	c := parseArgs(t, []string{"-shello"}) // -shello == -s hello
	if c.Flag("s") != "hello" {
		t.Errorf("-shello = %q, want hello", c.Flag("s"))
	}
}

func TestParseCombinedBoolThenValue(t *testing.T) {
	c := parseArgs(t, []string{"-as", "x"}) // -as x == -a -s x
	if !c.Bool("a") || c.Flag("s") != "x" {
		t.Errorf("-as x: a=%v s=%q", c.Bool("a"), c.Flag("s"))
	}
}

func TestParseDoubleDash(t *testing.T) {
	c := parseArgs(t, []string{"-a", "--", "-notaflag"})
	if !c.Bool("a") {
		t.Error("-a before -- should be set")
	}
	if len(c.Args) != 1 || c.Args[0] != "-notaflag" {
		t.Errorf("args after -- = %v", c.Args)
	}
}

func TestParseUnknownFlag(t *testing.T) {
	srv := tmux.NewServer(nil)
	if _, err := testCmd.parse(srv, []string{"-z"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Error("expected error for unknown flag -z")
	}
}

func TestParseMissingValue(t *testing.T) {
	srv := tmux.NewServer(nil)
	if _, err := testCmd.parse(srv, []string{"-s"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Error("expected error for -s without value")
	}
}

func TestParseArgBounds(t *testing.T) {
	min2 := &Command{Name: "m", MinArgs: 2, MaxArgs: 2}
	srv := tmux.NewServer(nil)
	if _, err := min2.parse(srv, []string{"only-one"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Error("expected error for too few args")
	}
	if _, err := min2.parse(srv, []string{"a", "b", "c"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Error("expected error for too many args")
	}
}

func TestUsageGeneration(t *testing.T) {
	got := testCmd.Usage()
	want := "test [-ab] [-n n] [-s value] [args...]"
	if got != want {
		t.Errorf("Usage() = %q, want %q", got, want)
	}
}
