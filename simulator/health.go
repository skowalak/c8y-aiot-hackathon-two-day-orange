package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// health is the state a Cumulocity probe sees. The platform restarts a
// microservice whose readiness endpoint does not answer, so the phase is
// reported rather than the process simply being up: a container stuck in
// "backfilling" for minutes is a diagnosis, not a failure.
type health struct {
	mu      sync.RWMutex
	phase   string
	started time.Time
}

func newHealth() *health {
	return &health{phase: "starting", started: time.Now()}
}

func (h *health) set(phase string) {
	h.mu.Lock()
	h.phase = phase
	h.mu.Unlock()
	slog.Info("phase", "phase", phase)
}

// serve starts the health endpoint and returns as soon as the socket is bound,
// so a caller can be sure the probe will find a listener.
func (h *health) serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		h.mu.RLock()
		phase := h.phase
		h.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "UP",
			"phase":   phase,
			"uptime":  time.Since(h.started).Round(time.Second).String(),
			"version": Version,
		})
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health endpoint failed", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("health endpoint listening", "addr", ln.Addr().String())
	return nil
}

// Version is stamped at build time and reported by /health so a rollout can be
// confirmed from outside.
var Version = "dev"

// env returns the environment variable or a fallback, used to give flags
// defaults that a container image can set.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// wantMQTT decides the live transport. MQTT is the honest choice for a device
// simulator, but it cannot be used inside the cluster: there the base URL is
// http://cumulocity:8111, an internal address with no MQTT broker behind it,
// and MQTT would additionally need device credentials from a bulk registration
// that nobody can upload to a running container. REST with the injected
// service user works in both places.
func wantMQTT(opt options) bool {
	switch opt.transport {
	case "mqtt":
		return true
	case "rest":
		return false
	}
	if inCluster(opt.baseURL) {
		slog.Info("running inside the platform, using REST for live telemetry")
		return false
	}
	return true
}

// inCluster reports whether this process is a Cumulocity microservice. The
// platform injects the isolation level, and rewrites the base URL to an
// internal service name that has no dot in it.
func inCluster(baseURL string) bool {
	if os.Getenv("C8Y_MICROSERVICE_ISOLATION") != "" {
		return true
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return !strings.Contains(host, ".")
}
