package agents

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status values for an agent run's lifecycle.
const (
	StatusReceived = "received" // delivery accepted by router (may or may not dispatch an agent)
	StatusQueued   = "queued"
	StatusRunning  = "running"
	StatusDone     = "done"
	StatusFailed   = "failed"
	StatusIgnored  = "ignored" // received but not routed (no mention / loop-guard / disabled)
)

// Event is one record in the router timeline: a received delivery and/or an agent run. Persisted as
// JSONL at <state>/agents/events.jsonl so the whole history survives restarts and is greppable.
type Event struct {
	ID       string    `json:"id"`
	TS       time.Time `json:"ts"`
	Kind     string    `json:"kind"`    // "delivery" | "route" | "result"
	Event    string    `json:"event"`   // forge event type (issue_comment, pull_request, manual)
	Repo     string    `json:"repo"`    // owner/name
	Issue    int       `json:"issue"`   // issue/PR number
	IsPull   bool      `json:"is_pull"` //
	Handle   string    `json:"handle"`  // routed agent handle ("" for pure delivery)
	Trigger  string    `json:"trigger"` // mention | manual | on_open
	Sender   string    `json:"sender"`  // who triggered it
	Status   string    `json:"status"`  //
	Detail   string    `json:"detail"`  // short human note
	Output   string    `json:"output"`  // bounded agent output (the posted review/handoff)
	Pushed   bool      `json:"pushed"`  // agent commits pushed back to the branch
	Duration float64   `json:"duration_s"`
}

// Store is the append-only JSONL event log with an in-memory tail for the UI.
type Store struct {
	mu   sync.Mutex
	path string
	seq  int64
}

// NewStore opens (creating the dir for) the events log under stateDir/agents/.
func NewStore(stateDir string) *Store {
	return &Store{path: filepath.Join(stateDir, "agents", "events.jsonl")}
}

// nextID returns a monotonically increasing id within this process (timestamp-seeded by Append's TS).
func (s *Store) nextID() string {
	s.seq++
	return time.Now().UTC().Format("20060102T150405") + "-" + itoa(s.seq)
}

// Append writes one event to the JSONL log (stamping ID + TS if unset) and returns it.
func (s *Store) Append(e Event) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.ID == "" {
		e.ID = s.nextID()
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err == nil {
		if fh, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			b, _ := json.Marshal(e)
			fh.Write(append(b, '\n'))
			fh.Close()
		}
	}
	return e
}

// Recent returns up to n most-recent events (newest first) read back from the log.
func (s *Store) Recent(n int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	fh, err := os.Open(s.path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	var all []Event
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			all = append(all, e)
		}
	}
	// newest first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if n > 0 && len(all) > n {
		all = all[:n]
	}
	return all
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
