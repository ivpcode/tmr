package web

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ivpcode/tmr/internal/ipc"
	"github.com/ivpcode/tmr/internal/ws"
)

// handleWS bridges one browser WebSocket to one daemon attach stream:
//
//	browser binary message  -> KindStdin
//	browser text message    -> control JSON {"cols":n,"rows":n} -> KindResize
//	KindOutput              -> browser binary message
//	KindDetached/Exit/Switch-> browser text JSON {"event":...} then close
//
// Closing either side detaches cleanly; the session keeps running.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cols := queryUint16(r, "cols", 80)
	rows := queryUint16(r, "rows", 24)

	unix, err := net.Dial("unix", s.cfg.Sock)
	if err != nil {
		http.Error(w, "daemon non raggiungibile", http.StatusBadGateway)
		return
	}

	wsc, err := ws.Upgrade(w, r)
	if err != nil {
		unix.Close()
		return
	}

	if err := ipc.Write(unix, &ipc.Frame{Kind: ipc.KindAttach, Session: name, Cols: cols, Rows: rows}); err != nil {
		wsc.Close()
		unix.Close()
		return
	}

	// Keepalive pings so idle terminals survive proxies and NAT timeouts.
	stopPing := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if wsc.Ping() != nil {
					return
				}
			case <-stopPing:
				return
			}
		}
	}()

	// Daemon -> browser.
	go func() {
		defer wsc.Close() // unblocks the browser reader below
		for {
			f, err := ipc.Read(unix)
			if err != nil {
				return
			}
			switch f.Kind {
			case ipc.KindOutput:
				if wsc.WriteMessage(ws.OpBinary, f.Data) != nil {
					return
				}
			case ipc.KindDetached, ipc.KindExit, ipc.KindSwitch:
				wsc.WriteMessage(ws.OpText, eventJSON(f))
				return
			}
		}
	}()

	// Browser -> daemon.
	for {
		op, data, err := wsc.ReadMessage()
		if err != nil {
			break
		}
		switch op {
		case ws.OpBinary:
			if ipc.Write(unix, &ipc.Frame{Kind: ipc.KindStdin, Data: data}) != nil {
				break
			}
		case ws.OpText:
			var ctl struct{ Cols, Rows uint16 }
			if json.Unmarshal(data, &ctl) == nil && ctl.Cols > 0 && ctl.Rows > 0 {
				ipc.Write(unix, &ipc.Frame{Kind: ipc.KindResize, Cols: ctl.Cols, Rows: ctl.Rows})
			}
		}
	}

	close(stopPing)
	ipc.Write(unix, &ipc.Frame{Kind: ipc.KindDetach}) // best effort
	unix.Close()
	wsc.Close()
}

// eventJSON renders a terminating attach event for the browser.
func eventJSON(f *ipc.Frame) []byte {
	m := map[string]any{}
	switch f.Kind {
	case ipc.KindExit:
		m["event"] = "exit"
		m["code"] = f.Code
		if f.Stderr != "" {
			m["error"] = f.Stderr
		}
	case ipc.KindSwitch:
		m["event"] = "switch"
		m["session"] = f.Session
	default:
		m["event"] = "detached"
	}
	out, _ := json.Marshal(m)
	return out
}

func queryUint16(r *http.Request, key string, def uint16) uint16 {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 1<<16 {
			return uint16(n)
		}
	}
	return def
}
