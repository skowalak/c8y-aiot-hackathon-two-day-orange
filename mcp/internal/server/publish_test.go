package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/c8y"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/server"
)

// fakeTenant accepts binary uploads, or rejects them when status is >= 300.
func fakeTenant(t *testing.T, status int) (*httptest.Server, *[]string) {
	t.Helper()
	var uploaded []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inventory/binaries" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			uploaded = append(uploaded, r.FormValue("object"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id":"4711"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &uploaded
}

func newPublisher(t *testing.T, status int, dir string) (*server.Publisher, *httptest.Server, *[]string) {
	t.Helper()
	tenant, uploaded := fakeTenant(t, status)
	client, err := c8y.New(c8y.Config{BaseURL: tenant.URL, Tenant: "t1", User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return server.NewPublisher(client, "https://mcp.example.com", dir), tenant, uploaded
}

func TestPublishServesTheShareableLink(t *testing.T) {
	dir := t.TempDir()
	pub, _, uploaded := newPublisher(t, http.StatusCreated, dir)

	html := []byte("<!DOCTYPE html><html><body>briefing</body></html>")
	links := pub.Publish(context.Background(), "Power Node 01", html,
		map[string]any{"device": "1", "scope": "shared-infrastructure"})

	if !strings.HasPrefix(links.Shareable, "https://mcp.example.com/reports/briefing-power-node-01-") {
		t.Fatalf("shareable = %q", links.Shareable)
	}
	if links.BinaryID != "4711" || !strings.HasSuffix(links.Binary, "/inventory/binaries/4711") {
		t.Fatalf("binary = %q (id %q)", links.Binary, links.BinaryID)
	}
	if len(*uploaded) != 1 || !strings.Contains((*uploaded)[0], "c8y_DiagnosticBriefing") {
		t.Fatalf("inventory metadata missing: %v", *uploaded)
	}

	// The link the agent hands out has to resolve without a Cumulocity login.
	srv := httptest.NewServer(pub.Handler())
	t.Cleanup(srv.Close)
	path := strings.TrimPrefix(links.Shareable, "https://mcp.example.com")

	resp, err := http.Get(srv.URL + path) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q", ct)
	}

	// And it has to survive a restart, i.e. come off disk, not just the cache.
	name := filepath.Base(path)
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Errorf("report not persisted: %v", err)
	}
}

func TestPublishSurvivesInventoryUploadFailure(t *testing.T) {
	pub, _, _ := newPublisher(t, http.StatusForbidden, "")

	links := pub.Publish(context.Background(), "node", []byte("<html></html>"), nil)

	if links.BinaryID != "" {
		t.Errorf("binary id = %q, want empty after a failed upload", links.BinaryID)
	}
	if links.Shareable == "" {
		t.Error("no shareable link, the locally hosted report should still work")
	}
}

func TestHandlerRejectsUnknownAndTraversalPaths(t *testing.T) {
	pub, _, _ := newPublisher(t, http.StatusCreated, t.TempDir())
	srv := httptest.NewServer(pub.Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{
		"/reports/does-not-exist.html",
		"/reports/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/reports/",
	} {
		resp, err := http.Get(srv.URL + path) //nolint:noctx // test client
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, resp.StatusCode)
		}
	}
}
