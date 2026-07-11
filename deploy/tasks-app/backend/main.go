package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "perform a self GET /healthz and exit 0/1")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck())
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://tasks:tasks@db:5432/tasks?sslmode=disable"
	}

	store, err := NewPostgresStore(dsn)
	if err != nil {
		// sql.Open only validates the DSN format; a failure here is a real
		// config error, so it is safe to exit.
		log.Fatalf("open store: %v", err)
	}

	// Create the schema in the background with retry so /healthz can answer
	// immediately and a transient DB delay never crash-loops the container.
	go ensureSchemaWithRetry(store)

	api := &API{store: store}
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Println("tasksapp listening on :8080")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func ensureSchemaWithRetry(store *PostgresStore) {
	backoff := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := store.EnsureSchema(ctx)
		cancel()
		if err == nil {
			log.Println("schema ready")
			return
		}
		log.Printf("schema not ready (attempt %d): %v", attempt, err)
		time.Sleep(backoff)
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func runHealthcheck() int {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:8080/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
