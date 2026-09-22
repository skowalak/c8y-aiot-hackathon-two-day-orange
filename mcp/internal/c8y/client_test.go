package c8y_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/c8y"
)

// recorder serves a tiny inventory and records every path it was asked for.
func recorder(t *testing.T) (*c8y.Client, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/identity/externalIds/c8y_Serial/sim-node-01"):
			_, _ = w.Write([]byte(`{"managedObject":{"id":"151156"}}`))
		case strings.HasPrefix(r.URL.Path, "/identity/externalIds/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		case r.URL.Path == "/inventory/managedObjects":
			// The inventory name carries a qualifier the operator does not say,
			// so only the prefix query finds it.
			if !strings.Contains(r.URL.RawQuery, "%2A") { // '*'
				_, _ = w.Write([]byte(`{"managedObjects":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"managedObjects":[
{"id":"151157","name":"Power Node 01 Group"},
{"id":"151156","name":"Power Node 01 (Feeder-A)","c8y_IsDevice":{}}]}`))
		case strings.HasPrefix(r.URL.Path, "/inventory/managedObjects/"):
			_, _ = w.Write([]byte(`{"id":"151156","name":"Power Node 01 (Feeder-A)","type":"sim_PowerNode",
"c8y_IsDevice":{},
"assetParents":{"references":[{"managedObject":{"id":"151157","name":"SIM Substation Alpha"}}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := c8y.New(c8y.Config{BaseURL: srv.URL, Tenant: "t1", User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return client, &paths
}

// On the microservice platform C8Y_BASEURL is an unreachable cluster-internal
// address, so links handed to a human must use the tenant's public domain.
func TestPublicBaseURLComesFromTheTenantDomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/currentTenant" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"t1","domainName":"two-day-orange.eu-latest.cumulocity.com"}`))
	}))
	t.Cleanup(srv.Close)

	client, err := c8y.New(c8y.Config{BaseURL: srv.URL, Tenant: "t1", User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	// Before the lookup the internal address is all we have.
	if got := client.PublicBaseURL(); got != srv.URL {
		t.Fatalf("public base = %q, want the internal %q", got, srv.URL)
	}

	public, err := client.ResolvePublicBaseURL(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	const want = "https://two-day-orange.eu-latest.cumulocity.com"
	if public != want {
		t.Errorf("public = %q, want %q", public, want)
	}
	if got := client.BinaryURL("4711"); got != want+"/inventory/binaries/4711" {
		t.Errorf("binary URL = %q, still internal?", got)
	}
	// API calls keep using the internal address, which is the fast path.
	if client.BaseURL() != srv.URL {
		t.Errorf("base URL changed to %q", client.BaseURL())
	}
}

func TestPublicBaseURLFallsBackWhenLookupFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	client, err := c8y.New(c8y.Config{BaseURL: srv.URL, Tenant: "t1", User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := client.ResolvePublicBaseURL(context.Background()); err == nil {
		t.Error("expected an error when the tenant lookup is denied")
	}
	if got := client.PublicBaseURL(); got != srv.URL {
		t.Errorf("public base = %q, want the internal %q as fallback", got, srv.URL)
	}
}

// Cumulocity returns empty assetParents unless withParents=true is requested,
// which silently disables neighbour discovery.
func TestResolveDeviceRequestsParents(t *testing.T) {
	for _, ref := range []string{"151156", "sim-node-01", "Power Node 01"} {
		t.Run(ref, func(t *testing.T) {
			client, paths := recorder(t)

			mo, err := client.ResolveDevice(context.Background(), ref)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if mo.ID != "151156" {
				t.Fatalf("id = %q", mo.ID)
			}
			if len(mo.AssetParents.References) != 1 {
				t.Fatalf("no parents resolved for %q", ref)
			}

			var got bool
			for _, p := range *paths {
				if strings.Contains(p, "/inventory/managedObjects/151156") &&
					strings.Contains(p, "withParents=true") {
					got = true
				}
			}
			if !got {
				t.Errorf("managed object fetched without withParents=true: %v", *paths)
			}
		})
	}
}

// Guessing between devices is worse than asking: a briefing about the wrong
// node is indistinguishable from a correct one to the reader.
func TestResolveDeviceRefusesAmbiguousName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/inventory/managedObjects" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"managedObjects":[
{"id":"1","name":"Power Node 01 (Feeder-A)","c8y_IsDevice":{}},
{"id":"2","name":"Power Node 02 (Feeder-A)","c8y_IsDevice":{}}]}`))
	}))
	t.Cleanup(srv.Close)

	client, err := c8y.New(c8y.Config{BaseURL: srv.URL, Tenant: "t1", User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_, err = client.ResolveDevice(context.Background(), "Power Node")
	if err == nil {
		t.Fatal("ambiguous name resolved to a single device")
	}
	if !strings.Contains(err.Error(), "matches several devices") {
		t.Fatalf("error does not name the candidates: %v", err)
	}
}
