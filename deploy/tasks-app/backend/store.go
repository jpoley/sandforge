package main

import (
	"context"
	"database/sql"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" sql driver
)

// Task is the domain model. JSON keys are camelCase per the API contract.
type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store abstracts persistence so handlers can be unit-tested with a fake.
type Store interface {
	List(ctx context.Context) ([]Task, error)
	Create(ctx context.Context, title string) (Task, error)
	SetDone(ctx context.Context, id string, done bool) (Task, error)
}

// PostgresStore is the production Store backed by Postgres.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore opens a *sql.DB using the pgx stdlib driver. It does not
// block on connectivity; callers should run EnsureSchema with retry in the
// background so /healthz can answer immediately.
func NewPostgresStore(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	return &PostgresStore{db: db}, nil
}

// EnsureSchema pings the DB and creates the tasks table if needed.
func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS tasks (
			id         text PRIMARY KEY,
			title      text NOT NULL,
			done       boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now()
		)`)
	return err
}

func (s *PostgresStore) List(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, done, created_at FROM tasks ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Done, &t.CreatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *PostgresStore) Create(ctx context.Context, title string) (Task, error) {
	t := Task{ID: newID(), Title: title, Done: false}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO tasks (id, title) VALUES ($1, $2) RETURNING created_at`,
		t.ID, t.Title).Scan(&t.CreatedAt)
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *PostgresStore) SetDone(ctx context.Context, id string, done bool) (Task, error) {
	var t Task
	err := s.db.QueryRowContext(ctx,
		`UPDATE tasks SET done = $1 WHERE id = $2
		 RETURNING id, title, done, created_at`,
		done, id).Scan(&t.ID, &t.Title, &t.Done, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return t, nil
}
