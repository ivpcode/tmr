package client

// tmux-style prefix key handling: Ctrl-B introduces a client command instead of
// reaching the application in the pty.
//
//	Ctrl-B d        detach (the session keeps running)
//	Ctrl-B n / )    switch to the next session
//	Ctrl-B p / (    switch to the previous session
//	Ctrl-B Ctrl-B   send a literal Ctrl-B to the application
//	Ctrl-B <other>  ignored, both bytes dropped (as tmux does)

const prefixKey = 0x02 // Ctrl-B

// Client actions produced by the prefix key.
const (
	actDetach byte = 'd'
	actNext   byte = 'n'
	actPrev   byte = 'p'
)

// keyFilter is the stateful scanner that separates prefix commands from bytes
// destined for the pty. The prefix state survives across reads, so Ctrl-B and
// its command key may arrive in different chunks.
type keyFilter struct {
	inPrefix bool
}

// Feed consumes one chunk of raw input and returns the bytes to forward to the
// pty plus any client actions triggered by prefix sequences.
func (k *keyFilter) Feed(data []byte) (out []byte, actions []byte) {
	for _, b := range data {
		if k.inPrefix {
			k.inPrefix = false
			switch b {
			case prefixKey:
				out = append(out, prefixKey) // Ctrl-B Ctrl-B -> literal Ctrl-B
			case 'd':
				actions = append(actions, actDetach)
			case 'n', ')':
				actions = append(actions, actNext)
			case 'p', '(':
				actions = append(actions, actPrev)
			}
			continue
		}
		if b == prefixKey {
			k.inPrefix = true
			continue
		}
		out = append(out, b)
	}
	return out, actions
}
