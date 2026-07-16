package command

// Runtime hooks let commands reach back into the running server for actions the
// data model alone can't express (e.g. kill-server stopping the accept loop).
// The server registers these at startup; in client-only contexts they are nil.

var shutdownHook func()

// SetShutdownHook registers the function kill-server should call to stop the
// daemon. Called once by the server at startup.
func SetShutdownHook(fn func()) { shutdownHook = fn }

// Shutdown asks the running server to exit. Safe to call when no hook is set.
func Shutdown() {
	if shutdownHook != nil {
		shutdownHook()
	}
}
