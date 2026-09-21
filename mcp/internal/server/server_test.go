package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/server"
)

// connect wires a client to the diagnostics server over an in-memory transport.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()

	srv := server.New(server.Options{})
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestToolContract(t *testing.T) {
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) != 1 {
		t.Fatalf("got %d tools, want exactly analyze_device_failure", len(res.Tools))
	}
	tool := res.Tools[0]

	if tool.Name != "analyze_device_failure" {
		t.Errorf("name = %q", tool.Name)
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Error("tool must advertise the read-only hint; it never writes to the tenant")
	}
	if len(tool.Description) < 80 {
		t.Error("description is too thin for a model to pick the tool reliably")
	}

	// The model has to know which argument is mandatory and how to phrase it.
	in := decodeSchema(t, tool.InputSchema)
	if got := in.Required; len(got) != 1 || got[0] != "device" {
		t.Errorf("required = %v, want [device]", got)
	}
	for _, prop := range []string{
		"device", "faultTime", "symptom", "beforeMinutes", "afterMinutes",
		"includeNeighbors", "maxNeighbors", "lookbackDays",
	} {
		p, ok := in.Properties[prop]
		if !ok {
			t.Errorf("input schema is missing %q", prop)
			continue
		}
		if p.Description == "" {
			t.Errorf("%q has no description", prop)
		}
	}
	if tool.OutputSchema == nil {
		t.Error("no output schema; the structured result would not be validated")
	}
}

// Cumulocity mounts the microservice at /service/<name> and strips that prefix
// before forwarding. The endpoint the SSE stream advertises must still carry
// it, otherwise the client posts its messages to the tenant root.
func TestSSEEndpointKeepsTheProxyPrefix(t *testing.T) {
	cases := []struct {
		name, fallback, header, want string
	}{
		{"configured prefix", "/service/diagnostic-agent-2", "", "/service/diagnostic-agent-2/sse?sessionid="},
		{"forwarded header wins", "/service/wrong", "/service/diagnostic-agent-2", "/service/diagnostic-agent-2/sse?sessionid="},
		{"bare deployment", "", "", "/sse?sessionid="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(server.Handler(server.Options{PathPrefix: tc.fallback}))
			t.Cleanup(srv.Close)

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/sse", nil)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			req.Header.Set("Accept", "text/event-stream")
			if tc.header != "" {
				req.Header.Set("X-Forwarded-Prefix", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("sse: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			buf := make([]byte, 256)
			n, _ := resp.Body.Read(buf)
			got := string(buf[:n])
			if !strings.Contains(got, "data: "+tc.want) {
				t.Errorf("endpoint event = %q, want data: %s…", got, tc.want)
			}
		})
	}
}

type schema struct {
	Required   []string `json:"required"`
	Properties map[string]struct {
		Description string `json:"description"`
	} `json:"properties"`
}

func decodeSchema(t *testing.T, raw any) schema {
	t.Helper()
	if raw == nil {
		t.Fatal("no schema")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var s schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return s
}

func TestHTTPRoutes(t *testing.T) {
	srv := httptest.NewServer(server.Handler(server.Options{}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/health") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health status = %d", resp.StatusCode)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/sse", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	sse, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	defer func() { _ = sse.Body.Close() }()

	if sse.StatusCode != http.StatusOK {
		t.Fatalf("sse status = %d", sse.StatusCode)
	}
	if ct := sse.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("sse content type = %q", ct)
	}
}
