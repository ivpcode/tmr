// Package web is the ivt web gateway: an HTTP server that lists sessions and
// exposes each one as an xterm.js terminal in the browser, bridged over
// WebSocket to the daemon's unix-socket IPC. The gateway is just another ivt
// client — it holds no session state of its own.
//
// Every request must present the access token, either as the ?t= query
// parameter (first visit; a cookie is then set) or as the ivt_t cookie.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/ivpcode/tmr/internal/client"
	"github.com/ivpcode/tmr/internal/ipc"
)

//go:embed assets
var assets embed.FS

// Config is the gateway configuration.
type Config struct {
	Addr  string // listen address from ParseAddr, e.g. ":9000" or "127.0.0.1:9000"
	Sock  string // ivt daemon unix socket path
	Token string // access token; generated if empty
}

// ParseAddr turns the web command's address argument into a listen address:
//
//	"9000"               -> ":9000"            (all interfaces)
//	"127.0.0.1:9000"     -> "127.0.0.1:9000"   (localhost only)
//	"192.168.1.234:9000" -> as given           (one interface)
//
// There is no default port: the argument is mandatory.
func ParseAddr(arg string) (string, error) {
	if p, err := strconv.Atoi(arg); err == nil {
		if p < 1 || p > 65535 {
			return "", fmt.Errorf("porta non valida: %s", arg)
		}
		return ":" + arg, nil
	}
	_, port, err := net.SplitHostPort(arg)
	if err != nil {
		return "", fmt.Errorf("indirizzo non valido %q: usa <porta> oppure <host:porta>", arg)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("porta non valida in %q", arg)
	}
	return arg, nil
}

// Server is the running gateway.
type Server struct {
	cfg Config
}

// NewToken returns a fresh random access token.
func NewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("web: cannot read random: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Run serves the gateway forever (until the listener fails). It always speaks
// HTTPS with a persistent self-signed certificate — never plain HTTP — and
// prints the ready-to-open URLs on stdout before blocking.
func Run(cfg Config) error {
	if cfg.Token == "" {
		cfg.Token = NewToken()
	}
	s := &Server{cfg: cfg}

	dir, err := certDir(cfg.Sock)
	if err != nil {
		return err
	}
	cert, certPath, err := loadOrCreateCert(dir)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	printURLs(ln.Addr().String(), cfg.Token)
	fmt.Printf("certificato autofirmato: %s (il browser chiede conferma al primo accesso)\n", certPath)

	tlsLn := tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	return http.Serve(tlsLn, s.handler())
}

// printURLs lists the URLs the gateway answers on: one for a specific host,
// or every local address when listening on all interfaces.
func printURLs(lnAddr, token string) {
	host, port, err := net.SplitHostPort(lnAddr)
	if err != nil {
		fmt.Printf("ivt web: https://%s/?t=%s\n", lnAddr, token)
		return
	}
	if host != "" && host != "::" && host != "0.0.0.0" {
		fmt.Printf("ivt web: https://%s/?t=%s\n", net.JoinHostPort(host, port), token)
		return
	}
	fmt.Println("ivt web in ascolto su tutte le interfacce:")
	fmt.Printf("  https://localhost:%s/?t=%s\n", port, token)
	for _, ip := range localIPs() {
		if ip.To4() != nil { // gli URL IPv4 sono i più comodi da copiare
			fmt.Printf("  https://%s/?t=%s\n", net.JoinHostPort(ip.String(), port), token)
		}
	}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.auth(s.handleIndex))
	mux.HandleFunc("GET /s/{name}", s.auth(s.handleTerminal))
	mux.HandleFunc("GET /ws/{name}", s.auth(s.handleWS))
	mux.HandleFunc("GET /api/sessions", s.auth(s.handleSessions))
	mux.HandleFunc("POST /api/new", s.auth(s.handleNew))
	mux.HandleFunc("POST /api/kill", s.auth(s.handleKill))
	mux.Handle("GET /assets/", s.authMiddleware(http.FileServer(http.FS(assets))))
	return mux
}

// ---- authentication --------------------------------------------------------

const tokenCookie = "ivt_t"

func (s *Server) authorized(r *http.Request) bool {
	if t := r.URL.Query().Get("t"); t != "" {
		return subtle.ConstantTimeCompare([]byte(t), []byte(s.cfg.Token)) == 1
	}
	if c, err := r.Cookie(tokenCookie); err == nil {
		return subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.cfg.Token)) == 1
	}
	return false
}

// auth wraps a handler with token/cookie checking; a valid ?t= sets the cookie
// so subsequent asset/WS requests carry it automatically.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "forbidden: token mancante o errato (aggiungi ?t=<token> all'URL)", http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("t") != "" {
			http.SetCookie(w, &http.Cookie{
				Name: tokenCookie, Value: s.cfg.Token,
				Path: "/", HttpOnly: true, Secure: true,
				SameSite: http.SameSiteStrictMode,
			})
		}
		next(w, r)
	}
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- daemon access ----------------------------------------------------------

// cmd runs one command on the daemon. A daemon that isn't running is reported
// as (nil frame, nil error) so callers can treat it as "no sessions".
func (s *Server) cmd(argv ...string) (*ipc.Frame, error) {
	if !client.ServerRunning(s.cfg.Sock) {
		return nil, nil
	}
	conn, err := net.Dial("unix", s.cfg.Sock)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := ipc.Write(conn, &ipc.Frame{Kind: ipc.KindCmd, Argv: argv, Cols: 80, Rows: 24}); err != nil {
		return nil, err
	}
	return ipc.Read(conn)
}

// ---- handlers ---------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(strings.ReplaceAll(terminalHTML, "{{NAME}}", htmlEscape(name))))
}

func (s *Server) handleSessions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp, err := s.cmd("ls", "-j")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if resp == nil || strings.TrimSpace(resp.Stdout) == "" {
		w.Write([]byte("[]"))
		return
	}
	w.Write([]byte(resp.Stdout))
}

func (s *Server) handleNew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string   `json:"name"`
		Cmd  []string `json:"cmd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := client.EnsureServer(s.cfg.Sock); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	argv := append([]string{"new", req.Name}, req.Cmd...)
	if req.Name == "" {
		http.Error(w, "nome sessione obbligatorio", http.StatusBadRequest)
		return
	}
	resp, err := s.cmd(argv...)
	if err != nil || resp == nil {
		http.Error(w, "daemon non raggiungibile", http.StatusBadGateway)
		return
	}
	if resp.Code != 0 {
		http.Error(w, strings.TrimSpace(resp.Stderr), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": strings.TrimSpace(resp.Stdout)})
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	resp, err := s.cmd("kill", req.Name)
	if err != nil || resp == nil {
		http.Error(w, "daemon non raggiungibile", http.StatusBadGateway)
		return
	}
	if resp.Code != 0 {
		http.Error(w, strings.TrimSpace(resp.Stderr), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
