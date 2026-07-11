// Package logx provides structured JSONL logging for Sandforge: a human-readable console
// stream plus an append-only decision/event log under .logs/ (repo execution rules).
package logx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger writes events as JSONL to .logs/events/<date>.jsonl and mirrors a concise line to
// the console. Decisions go to .logs/decisions/<date>.jsonl (docs/loop.md Prompt 8).
type Logger struct {
	mu      sync.Mutex
	root    string // the directory under which .logs/ is written (the instance state dir)
	quiet   bool
	console *os.File
}

// New returns a Logger rooted at the given directory (where .logs/ lives).
func New(root string, quiet bool) *Logger {
	return &Logger{root: root, quiet: quiet, console: os.Stderr}
}

func nowUTC() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func (l *Logger) appendJSONL(sub string, rec map[string]any) {
	dir := filepath.Join(l.root, ".logs", sub)
	_ = os.MkdirAll(dir, 0o755)
	f := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	b, _ := json.Marshal(rec)
	fh.Write(append(b, '\n'))
}

// Event logs a structured event (phase/action with fields) to .logs/events and the console.
func (l *Logger) Event(kind, msg string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := map[string]any{"ts": nowUTC(), "kind": kind, "msg": msg}
	for k, v := range fields {
		rec[k] = v
	}
	l.appendJSONL("events", rec)
	if !l.quiet {
		extra := ""
		if len(fields) > 0 {
			b, _ := json.Marshal(fields)
			extra = " " + string(b)
		}
		fmt.Fprintf(l.console, "  • %s%s\n", msg, extra)
	}
}

// Decision logs an architectural/operational decision to .logs/decisions (JSONL).
func (l *Logger) Decision(id, decision, rationale string, refs ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := map[string]any{
		"ts": nowUTC(), "id": id, "actor": "sandforge",
		"decision": decision, "rationale": rationale, "ref": refs,
	}
	l.appendJSONL("decisions", rec)
}

// Step prints a top-level progress header to the console (and logs it).
func (l *Logger) Step(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	l.Event("step", msg, nil)
}

// Infof / Warnf print to console without an event record.
func (l *Logger) Infof(format string, a ...any) {
	if !l.quiet {
		fmt.Fprintf(l.console, format+"\n", a...)
	}
}
