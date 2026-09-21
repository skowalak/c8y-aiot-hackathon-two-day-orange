// Command mcpd is the Automated Root Cause & Diagnostics MCP server for
// Cumulocity IoT. It exposes analyze_device_failure over SSE to the AI Agent
// Manager and hosts the generated diagnostic briefings.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/c8y"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/diag"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/report"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("mcpd failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr       = flag.String("addr", envOr("MCP_ADDR", ":8080"), "listen address")
		publicURL  = flag.String("public-url", os.Getenv("MCP_PUBLIC_URL"), "externally reachable base URL of this service, used for report links")
		service    = flag.String("service-name", os.Getenv("APPLICATION_NAME"), "microservice name; report links are derived as <baseurl>/service/<name> when -public-url is unset")
		reportsDir = flag.String("reports-dir", os.Getenv("MCP_REPORTS_DIR"), "optional directory to persist generated briefings; unsupported on the microservice platform, which has no volumes")
		baseURL    = flag.String("baseurl", os.Getenv("C8Y_BASEURL"), "Cumulocity base URL (env C8Y_BASEURL)")
		tenant     = flag.String("tenant", os.Getenv("C8Y_TENANT"), "tenant ID (env C8Y_TENANT)")
		user       = flag.String("user", os.Getenv("C8Y_USER"), "user name (env C8Y_USER)")
		password   = flag.String("password", os.Getenv("C8Y_PASSWORD"), "password (env C8Y_PASSWORD)")
		before     = flag.Duration("before", 2*time.Hour, "default history analysed before the fault")
		after      = flag.Duration("after", 30*time.Minute, "default history analysed after the fault")
		neighbors  = flag.Int("max-neighbors", 6, "default maximum number of neighbouring assets")
		smartRules = flag.Bool("smart-rules", false, "additionally query the smart rule service for threshold rules")
		analyze    = flag.String("analyze", "", "run one analysis for this device on the command line and exit")
		faultTime  = flag.String("fault", "", "fault timestamp for -analyze (RFC3339 or an offset like -3h, default now)")
		outFile    = flag.String("out", "", "write the HTML briefing of -analyze to this file")
		verbose    = flag.Bool("v", false, "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	client, err := c8y.New(c8y.Config{
		BaseURL: *baseURL, Tenant: *tenant, User: *user, Password: *password,
	})
	if err != nil {
		return err
	}
	engine := diag.NewEngine(client)
	engine.UseSmartRules = *smartRules

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// On the microservice platform C8Y_BASEURL is an internal cluster address
	// that nobody outside can open, so the tenant's real domain has to be
	// looked up before any link is handed out.
	if public, err := client.ResolvePublicBaseURL(ctx); err != nil {
		slog.Warn("public tenant domain unknown, report links may be internal",
			"url", public, "err", err)
	} else if public != client.BaseURL() {
		slog.Info("resolved public tenant domain", "url", public)
	}

	// Behind the platform proxy the service is reachable at /service/<name>,
	// and that prefix has to end up both in the report links we hand to the
	// agent and in the endpoint the SSE transport advertises.
	var prefix string
	if *service != "" {
		prefix = "/service/" + *service
		if *publicURL == "" {
			*publicURL = client.PublicBaseURL() + prefix
		}
	}

	if *analyze != "" {
		return analyzeOnce(ctx, engine, *analyze, *faultTime, *before, *after, *neighbors, *outFile)
	}

	opt := server.Options{
		Engine:        engine,
		Publisher:     server.NewPublisher(client, *publicURL, *reportsDir),
		DefaultBefore: *before,
		DefaultAfter:  *after,
		MaxNeighbors:  *neighbors,
		PathPrefix:    prefix,
	}
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(opt),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	slog.Info("mcp server listening",
		"addr", *addr, "sse", "/sse", "reports", "/reports/", "tenant", client.BaseURL())
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// analyzeOnce runs the engine from the command line, which is handy to test
// the diagnosis without an MCP client.
func analyzeOnce(ctx context.Context, engine *diag.Engine, device, faultTime string, before, after time.Duration, neighbors int, outFile string) error {
	req := diag.Request{
		Device: device, Before: before, After: after,
		IncludeNeighbors: true, MaxNeighbors: neighbors,
	}
	if faultTime != "" {
		t, err := server.ParseTime(faultTime)
		if err != nil {
			return fmt.Errorf("-fault: %w", err)
		}
		req.FaultTime = t
	}
	briefing, err := engine.Analyze(ctx, req)
	if err != nil {
		return err
	}
	fmt.Println(report.Markdown(briefing))

	if outFile != "" {
		html, err := report.HTML(briefing)
		if err != nil {
			return err
		}
		if err := os.WriteFile(outFile, html, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", outFile, err)
		}
		slog.Info("briefing written", "path", outFile, "bytes", len(html))
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
