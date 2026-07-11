package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// ErrNotFound signals that a requested task does not exist.
var ErrNotFound = errors.New("task not found")

// newID returns a random 128-bit hex id (non-empty string).
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// rand.Read should never fail; fall back is still non-empty.
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

// API holds the dependencies for the HTTP handlers.
type API struct {
	store Store
}

// Routes builds the chi router for the application.
func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", a.health)
	r.Route("/api/tasks", func(r chi.Router) {
		r.Get("/", a.listTasks)
		r.Post("/", a.createTask)
		r.Patch("/{id}", a.patchTask)
	})
	return r
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (a *API) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := a.store.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

type createTaskRequest struct {
	Title string `json:"title"`
}

func (a *API) createTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	task, err := a.store.Create(r.Context(), req.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task")
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

type patchTaskRequest struct {
	Done *bool `json:"done"`
}

func (a *API) patchTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req patchTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Done == nil {
		writeError(w, http.StatusBadRequest, "done is required")
		return
	}
	task, err := a.store.SetDone(r.Context(), id, *req.Done)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update task")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
