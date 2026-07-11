package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory Store for hermetic handler tests (no DB).
type fakeStore struct {
	mu    sync.Mutex
	tasks []Task
	seq   int
}

func (f *fakeStore) List(ctx context.Context) ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Task, len(f.tasks))
	copy(out, f.tasks)
	return out, nil
}

func (f *fakeStore) Create(ctx context.Context, title string) (Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	t := Task{ID: newID(), Title: title, Done: false, CreatedAt: time.Now()}
	f.tasks = append(f.tasks, t)
	return t, nil
}

func (f *fakeStore) SetDone(ctx context.Context, id string, done bool) (Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.tasks {
		if f.tasks[i].ID == id {
			f.tasks[i].Done = done
			return f.tasks[i], nil
		}
	}
	return Task{}, ErrNotFound
}

func newTestServer() http.Handler {
	return (&API{store: &fakeStore{}}).Routes()
}

func TestHealthz(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestCreateAndList(t *testing.T) {
	srv := newTestServer()

	// Empty list to start.
	var initial []Task
	doJSON(t, srv, http.MethodGet, "/api/tasks/", nil, http.StatusOK, &initial)
	if len(initial) != 0 {
		t.Fatalf("expected empty list, got %d", len(initial))
	}

	// Create returns 201 with non-empty id and done=false.
	var created Task
	doJSON(t, srv, http.MethodPost, "/api/tasks/",
		map[string]string{"title": "buy milk"}, http.StatusCreated, &created)
	if created.ID == "" {
		t.Fatal("created.ID is empty")
	}
	if created.Title != "buy milk" {
		t.Fatalf("title = %q", created.Title)
	}
	if created.Done {
		t.Fatal("new task should not be done")
	}

	// List now reflects the write.
	var listed []Task
	doJSON(t, srv, http.MethodGet, "/api/tasks/", nil, http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list does not reflect create: %+v", listed)
	}
}

func TestCreateRejectsEmptyTitle(t *testing.T) {
	srv := newTestServer()
	rec := raw(srv, http.MethodPost, "/api/tasks/", map[string]string{"title": "   "})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPatchTogglesDone(t *testing.T) {
	srv := newTestServer()

	var created Task
	doJSON(t, srv, http.MethodPost, "/api/tasks/",
		map[string]string{"title": "task"}, http.StatusCreated, &created)

	var patched Task
	doJSON(t, srv, http.MethodPatch, "/api/tasks/"+created.ID,
		map[string]bool{"done": true}, http.StatusOK, &patched)
	if !patched.Done {
		t.Fatal("patched task should be done")
	}

	// Subsequent GET reflects the change.
	var listed []Task
	doJSON(t, srv, http.MethodGet, "/api/tasks/", nil, http.StatusOK, &listed)
	if len(listed) != 1 || !listed[0].Done {
		t.Fatalf("list does not reflect patch: %+v", listed)
	}
}

func TestPatchUnknownIs404(t *testing.T) {
	srv := newTestServer()
	rec := raw(srv, http.MethodPatch, "/api/tasks/does-not-exist", map[string]bool{"done": true})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestJSONKeysAreCamelCase(t *testing.T) {
	srv := newTestServer()
	rec := raw(srv, http.MethodPost, "/api/tasks/", map[string]string{"title": "x"})
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "title", "done", "createdAt"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing JSON key %q in %v", k, m)
		}
	}
}

func raw(srv http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func doJSON(t *testing.T, srv http.Handler, method, path string, body any, wantStatus int, out any) {
	t.Helper()
	rec := raw(srv, method, path, body)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: status = %d, want %d (body %s)", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
		}
	}
}
