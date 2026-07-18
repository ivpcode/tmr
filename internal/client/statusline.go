package client

import (
	"fmt"
	"strings"
	"time"
)

// The tmux-style status line: the pty is one row shorter than the real
// terminal, a DECSTBM scroll region keeps application output above the last
// row, and the client redraws the bar there every second (blue background,
// session name on the left, time and date on the right).

const (
	saveCursor    = "\x1b7"
	restoreCursor = "\x1b8"
	resetRegion   = "\x1b[r"
	barStyle      = "\x1b[0;44;97m" // bright white on blue

	pushTitle = "\x1b[22;0t" // save the window title on the terminal's title stack
	popTitle  = "\x1b[23;0t" // restore it (best effort: not every emulator supports the stack)
)

// setTitle sets the terminal window/tab title to the session name (OSC 0 sets
// both icon and window title). Re-emitted every second, so a title set by the
// application inside the session gets overridden — the emulator's tab always
// shows which ivt session this is.
func setTitle(name string) []byte { return []byte("\x1b]0;" + name + "\x07") }

// setRegion confines scrolling to rows 1..n (the row below hosts the bar).
func setRegion(n int) string { return fmt.Sprintf("\x1b[1;%dr", n) }

// statusLine renders the bar at screen row `row`, padded/truncated to cols,
// without moving the application cursor (save/restore around the draw).
func statusLine(cols, row int, name string, now time.Time) []byte {
	left := " [" + name + "]"
	right := now.Format("15:04  02/01/2006") + " "

	lw, rw := len([]rune(left)), len([]rune(right))
	pad := cols - lw - rw
	if pad < 1 {
		// Narrow terminal: keep the session name, drop the clock.
		right = ""
		if lw > cols {
			left = string([]rune(left)[:cols])
		}
		pad = cols - len([]rune(left))
	}

	return []byte(saveCursor +
		fmt.Sprintf("\x1b[%d;1H", row) +
		barStyle + left + strings.Repeat(" ", pad) + right + "\x1b[0m" +
		restoreCursor)
}

// clearBar wipes the bar row and resets the scroll region, leaving the cursor
// on the (now empty) bottom row — used when the attach ends.
func clearBar(row int) []byte {
	return []byte(fmt.Sprintf("\x1b[%d;1H\x1b[2K", row) + resetRegion)
}
