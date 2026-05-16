// Package main is an example service for the k3d-argocd-template repo.
//
// A small CRUD API over a Postgres-backed `items` table, with the operational
// signals an SRE expects: structured logs, Prometheus metrics, liveness and
// readiness probes, graceful shutdown, and DB-failure-aware readiness gating.
//
// Single file by design — easier to read end-to-end as a template.
// Larger projects typically split into cmd/, internal/handlers, internal/db, etc.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Build-time metadata, set via -ldflags.
var (
	version = "dev"
	commit  = "none"
)

// ────────────────────────────────────────────────────────────────────────────
// Configuration
// ────────────────────────────────────────────────────────────────────────────

type config struct {
	listenAddr     string
	dbURL          string
	logLevel       slog.Level
	shutdownGrace  time.Duration
	dbConnectTries int
}

func loadConfig() config {
	return config{
		listenAddr:     getEnv("LISTEN_ADDR", ":8080"),
		dbURL:          mustEnv("DB_URL"),
		logLevel:       parseLogLevel(getEnv("LOG_LEVEL", "info")),
		shutdownGrace:  parseDuration(getEnv("SHUTDOWN_GRACE", "10s")),
		dbConnectTries: 30,
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v, ok := os.LookupEnv(key)
	if !ok {
		fmt.Fprintf(os.Stderr, "required env var %s not set\n", key)
		os.Exit(2)
	}
	return v
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 10 * time.Second
	}
	return d
}

// ────────────────────────────────────────────────────────────────────────────
// Migrations (embedded)
// ────────────────────────────────────────────────────────────────────────────

//go:embed migrations/*.sql
var migrationFS embed.FS

func runMigrations(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		log.Info("applying migration", "file", e.Name())
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("apply migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Database connection
// ────────────────────────────────────────────────────────────────────────────

func connectDB(ctx context.Context, cfg config, log *slog.Logger) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	// Reasonable defaults for a small service. Tune based on observed load.
	pcfg.MaxConns = 10
	pcfg.MinConns = 1
	pcfg.MaxConnLifetime = 30 * time.Minute
	pcfg.HealthCheckPeriod = 30 * time.Second

	var pool *pgxpool.Pool
	for i := 1; i <= cfg.dbConnectTries; i++ {
		pool, err = pgxpool.NewWithConfig(ctx, pcfg)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				log.Info("connected to db", "attempt", i)
				return pool, nil
			}
			pool.Close()
		}
		log.Warn("db connect attempt failed", "attempt", i, "err", err)
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("after %d attempts: %w", cfg.dbConnectTries, err)
}

// ────────────────────────────────────────────────────────────────────────────
// Domain
// ────────────────────────────────────────────────────────────────────────────

type Item struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// ────────────────────────────────────────────────────────────────────────────
// Server / handlers
// ────────────────────────────────────────────────────────────────────────────

type server struct {
	pool  *pgxpool.Pool
	log   *slog.Logger
	ready atomic.Bool // flipped to true once startup completes
}

// /healthz — liveness. Always 200 if the process is alive.
// Liveness intentionally does NOT check the DB; the kubelet should not restart
// the pod just because Postgres is temporarily unavailable.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// /readyz — readiness. 200 only if startup is complete AND DB is reachable.
// Readiness gates traffic, so when the DB is down we shed load gracefully
// rather than serving requests that will fail.
func (s *server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		http.Error(w, "starting up", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		s.log.Warn("readyz: db ping failed", "err", err)
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func (s *server) handleListItems(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.pool.Query(ctx, "SELECT id, name, created_at FROM items ORDER BY id")
	if err != nil {
		s.log.Error("list items: query failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	out := make([]Item, 0)
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Name, &it.CreatedAt); err != nil {
			s.log.Error("list items: scan failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "db error")
			return
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("list items: rows err", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "db error")
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleCreateItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeJSONError(w, http.StatusBadRequest, "name required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var it Item
	err := s.pool.QueryRow(ctx,
		`INSERT INTO items (name) VALUES ($1) RETURNING id, name, created_at`,
		in.Name,
	).Scan(&it.ID, &it.Name, &it.CreatedAt)
	if err != nil {
		s.log.Error("create item: insert failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "db error")
		return
	}

	s.log.Info("item created", "id", it.ID, "name", it.Name)
	writeJSON(w, http.StatusCreated, it)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ────────────────────────────────────────────────────────────────────────────
// Metrics
// ────────────────────────────────────────────────────────────────────────────

var (
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "myapp",
			Name:      "http_requests_total",
			Help:      "Total HTTP requests by method, path, status.",
		},
		[]string{"method", "path", "status"},
	)
	httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "myapp",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
			Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
		[]string{"method", "path"},
	)
	buildInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "myapp",
			Name:      "build_info",
			Help:      "Static build info; value is always 1.",
		},
		[]string{"version", "commit"},
	)
)

// statusRecorder captures the status code for the metrics middleware.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}

func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip /metrics itself — Prometheus already knows it scraped.
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()
		path := normalizePath(r.URL.Path)
		httpRequests.WithLabelValues(r.Method, path, fmt.Sprintf("%d", rec.status)).Inc()
		httpDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}

// normalizePath collapses dynamic segments to keep label cardinality bounded.
// /items/123 → /items/:id
func normalizePath(p string) string {
	switch {
	case p == "" || p == "/":
		return "/"
	case p == "/items":
		return "/items"
	case strings.HasPrefix(p, "/items/"):
		return "/items/:id"
	default:
		return p
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.logLevel}))
	slog.SetDefault(log)
	log.Info("starting", "version", version, "commit", commit, "addr", cfg.listenAddr)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Connect to DB with retry — at startup the DB may still be coming up.
	pool, err := connectDB(rootCtx, cfg, log)
	if err != nil {
		log.Error("db connect failed after retries", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := runMigrations(rootCtx, pool, log); err != nil {
		log.Error("migrations failed", "err", err)
		os.Exit(1)
	}

	// Wire metrics into a private registry so we don't pollute the default one.
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpRequests,
		httpDuration,
		buildInfo,
	)
	buildInfo.WithLabelValues(version, commit).Set(1)

	// HTTP routing — Go 1.22+ method-aware ServeMux, no router dependency needed.
	s := &server{pool: pool, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /items", s.handleListItems)
	mux.HandleFunc("POST /items", s.handleCreateItem)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	srv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           metricsMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Mark ready once startup work is done.
	s.ready.Store(true)
	log.Info("ready to serve")

	// Graceful shutdown on SIGINT / SIGTERM.
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stopCh <- syscall.SIGTERM
		}
	}()

	sig := <-stopCh
	log.Info("shutting down", "signal", sig.String())

	// Flip ready off so the load balancer drains traffic before we close.
	s.ready.Store(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", "err", err)
		os.Exit(1)
	}
	log.Info("clean exit")
}
