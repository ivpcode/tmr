package tmux

import (
	"errors"
	"os"
	"os/exec"
)

// loginShell returns the user's shell, falling back to /bin/sh.
func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// childEnv is the environment for session processes: the server's environment
// with TERM pinned to our fixed modern-terminal profile.
func childEnv() []string {
	env := os.Environ()
	out := env[:0]
	for _, e := range env {
		if len(e) >= 5 && e[:5] == "TERM=" {
			continue
		}
		out = append(out, e)
	}
	return append(out, "TERM=xterm-256color")
}

// waitCode turns a Wait error into a process exit code (0 on clean exit).
func waitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
