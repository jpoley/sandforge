package agents

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Server exposes the webhook sink, a small JSON API, and the embedded UI. It is intentionally
// dependency-light: all state lives in Config + Store, both file-backed.
type Server struct {
	Router *Router
	// WebhookURL is the forge-reachable target (e.g. http://host.docker.internal:3999/webhook),
	// shown in the UI so the human knows what was registered.
	WebhookURL string
	// ForgeURL is the human-facing (loopback) Forgejo URL, so the UI can link out to the forge.
	ForgeURL string
	// DocsMarkdown is the local-agents user guide, served to the UI's Docs tab.
	DocsMarkdown string
	// Logf is an optional sink for one-line server notes (defaults to no-op).
	Logf func(format string, a ...any)
	// LoopbackGuard restricts the UI + command-executing API routes to loopback callers. Enabled for
	// the host process (the listener binds all interfaces so the container can deliver webhooks, so
	// the dangerous routes must be loopback-gated). DISABLED in container mode: docker publishes the
	// port as 127.0.0.1:<port> on the host, so the host-side publish IS the boundary, and inside the
	// container even local traffic arrives from the bridge gateway (non-loopback) — guarding there
	// would 403 the legitimate local UI.
	LoopbackGuard bool
}

func (s *Server) logf(format string, a ...any) {
	if s.Logf != nil {
		s.Logf(format, a...)
	}
}

// Handler returns the http.Handler wiring every route.
//
// Security model (design §9/§11): the listener must bind a host-reachable interface so the forge
// CONTAINER can POST deliveries (via host.docker.internal) — it cannot be loopback-only. But the
// UI + the mutating API (/api/agents, /api/trigger) run agent commands on the host, so exposing
// them to the LAN would be remote code execution. Only /webhook is reachable off-loopback (and it
// is HMAC-gated); every other route is restricted to loopback callers — the local human's browser.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", s.handleWebhook)
	mux.HandleFunc("/api/agents", s.loopback(s.handleAgents))
	mux.HandleFunc("/api/agents/", s.loopback(s.handleAgentDelete))
	mux.HandleFunc("/api/events", s.loopback(s.handleEvents))
	mux.HandleFunc("/api/trigger", s.loopback(s.handleTrigger))
	mux.HandleFunc("/api/status", s.loopback(s.handleStatus))
	mux.HandleFunc("/api/docs", s.loopback(s.handleDocs))
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/", s.loopback(s.handleUI()))
	return mux
}

// loopback wraps a handler so only requests from the loopback interface reach it. A non-loopback
// caller (the LAN) gets 403 — these routes can execute host commands, so they are local-human-only.
func (s *Server) loopback(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.LoopbackGuard && !isLoopbackRemote(r.RemoteAddr) {
			s.logf("blocked non-loopback %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			http.Error(w, "forbidden: this endpoint is local-only (loopback)", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// isLoopbackRemote reports whether a net/http RemoteAddr ("host:port") is a loopback address.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Forgejo sends X-Forgejo-Event + X-Forgejo-Signature (also X-Hub-Signature-256).
	eventType := firstHeader(r, "X-Forgejo-Event", "X-Gitea-Event", "X-GitHub-Event")
	sig := firstHeader(r, "X-Forgejo-Signature", "X-Gitea-Signature", "X-Hub-Signature-256")
	if _, err := s.Router.HandleDelivery(eventType, body, sig); err != nil {
		// A verification/parse failure must be visible, not swallowed. 202 is reserved for accepted
		// deliveries; surface real problems as 400 so a misconfigured hook is obvious in Forgejo's UI.
		s.logf("webhook %s rejected: %v", eventType, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	io.WriteString(w, "accepted")
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.Router.Cfg.Snapshot())
	case http.MethodPost:
		var a Agent
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
			http.Error(w, "decode agent: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Router.Cfg.Upsert(a); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, s.Router.Cfg.Snapshot())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAgentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}
	handle := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	if err := s.Router.Cfg.Remove(handle); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.Router.Cfg.Snapshot())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	writeJSON(w, http.StatusOK, s.Router.Store.Recent(n))
}

func (s *Server) handleTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Owner   string `json:"owner"`
		Repo    string `json:"repo"`
		Issue   int    `json:"issue"`
		IsPull  bool   `json:"is_pull"`
		Handle  string `json:"handle"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "decode trigger: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Owner == "" || req.Repo == "" || req.Issue <= 0 || req.Handle == "" {
		http.Error(w, "owner, repo, issue (>0) and handle are required", http.StatusBadRequest)
		return
	}
	ev, err := s.Router.Trigger(req.Owner, req.Repo, req.Issue, req.IsPull, req.Handle, req.Comment)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"webhook_url":  s.WebhookURL,
		"forge_url":    s.ForgeURL,
		"secret_set":   s.Router.Cfg.Secret != "",
		"bot_login":    s.Router.Cfg.BotLogin,
		"agent_count":  len(s.Router.Cfg.Snapshot()),
		"async":        s.Router.Async,
		"agent_marker": AgentMarker,
	})
}

// handleDocs serves the local-agents user guide (markdown) for the UI's Docs tab.
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if s.DocsMarkdown == "" {
		io.WriteString(w, "# Documentation unavailable\n\nThe embedded user guide was not found.")
		return
	}
	io.WriteString(w, s.DocsMarkdown)
}

// handleUI serves the embedded React/shadcn SPA: static assets by path, with an index.html fallback
// for any non-asset path (client-side routing). Built once at compile time from deploy/agents-ui.
func (s *Server) handleUI() http.HandlerFunc {
	sub, err := uiFS()
	if err != nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "ui assets unavailable: "+err.Error(), http.StatusInternalServerError)
		}
	}
	fileServer := http.FileServer(http.FS(sub))
	index, _ := fs.ReadFile(sub, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := sub.Open(p); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: serve index.html for "/" and any unknown route.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	}
}

func firstHeader(r *http.Request, keys ...string) string {
	for _, k := range keys {
		if v := r.Header.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, fmt.Sprintf("encode response: %v", err), http.StatusInternalServerError)
		return
	}
	w.Write(b)
}
