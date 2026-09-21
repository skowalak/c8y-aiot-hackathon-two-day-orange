package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/c8y"
)

// Links points to the published artefact.
type Links struct {
	// Shareable is the link handed to the user in chat.
	Shareable string
	// Binary is the Cumulocity inventory binary URL (requires tenant login).
	Binary string
	// BinaryID is the inventory binary managed object ID.
	BinaryID string
}

// Publisher stores generated briefings in the Cumulocity inventory binary
// repository and serves a copy from this microservice.
type Publisher struct {
	client    *c8y.Client
	publicURL string
	dir       string

	mu     sync.RWMutex
	memory map[string][]byte
}

// NewPublisher returns a publisher. dir may be empty to keep reports in memory
// only; publicURL is the externally reachable base URL of this service.
func NewPublisher(client *c8y.Client, publicURL, dir string) *Publisher {
	return &Publisher{
		client:    client,
		publicURL: strings.TrimRight(publicURL, "/"),
		dir:       dir,
		memory:    map[string][]byte{},
	}
}

// Publish stores the document and returns the links to it.
func (p *Publisher) Publish(ctx context.Context, slug string, html []byte, meta map[string]any) Links {
	id := reportID(slug)
	name := id + ".html"

	p.mu.Lock()
	p.memory[id] = html
	p.mu.Unlock()

	if p.dir != "" {
		if err := os.MkdirAll(p.dir, 0o750); err != nil {
			slog.Warn("report dir not writable", "dir", p.dir, "err", err)
		} else if err := os.WriteFile(filepath.Join(p.dir, name), html, 0o600); err != nil {
			slog.Warn("report not persisted", "err", err)
		}
	}

	links := Links{}
	if p.publicURL != "" {
		links.Shareable = fmt.Sprintf("%s/reports/%s", p.publicURL, name)
	}

	fragments := map[string]any{
		"c8y_IsBinary":           map[string]any{},
		"c8y_DiagnosticBriefing": meta,
	}
	binaryID, err := p.client.UploadBinary(ctx, name, "text/html", html, fragments)
	if err != nil {
		slog.Warn("briefing not uploaded to inventory binaries", "err", err)
	} else {
		links.BinaryID = binaryID
		links.Binary = p.client.BinaryURL(binaryID)
		if links.Shareable == "" {
			links.Shareable = links.Binary
		}
	}
	slog.Info("briefing published", "id", id, "bytes", len(html),
		"binary", links.BinaryID, "url", links.Shareable)
	return links
}

// Handler serves the locally cached reports.
func (p *Publisher) Handler() http.Handler {
	return http.StripPrefix("/reports/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(filepath.Base(r.URL.Path), ".html")
		p.mu.RLock()
		data, ok := p.memory[name]
		p.mu.RUnlock()

		if !ok && p.dir != "" {
			if b, err := os.ReadFile(filepath.Join(p.dir, name+".html")); err == nil {
				data, ok = b, true
			}
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}))
}

func reportID(slug string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		case r == ' ' || r == '_' || r == '.':
			return '-'
		default:
			return -1
		}
	}, slug)
	if clean == "" {
		clean = "device"
	}
	return fmt.Sprintf("briefing-%s-%s-%s",
		clean, time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]))
}
