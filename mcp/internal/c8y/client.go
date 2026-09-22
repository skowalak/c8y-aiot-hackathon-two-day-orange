// Package c8y is the read oriented Cumulocity client of the diagnostics MCP
// server: inventory lookups, telemetry/alarm/event queries and binary upload.
package c8y

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config holds the tenant connection parameters.
type Config struct {
	BaseURL  string
	Tenant   string
	User     string
	Password string
}

// Client talks to the Cumulocity REST API with basic authentication.
type Client struct {
	base   *url.URL
	tenant string
	user   string
	auth   string
	hc     *http.Client

	mu         sync.RWMutex
	publicBase string
}

// New validates the configuration and returns a client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("c8y: base URL is required")
	}
	if cfg.User == "" || cfg.Password == "" {
		return nil, errors.New("c8y: user and password are required")
	}
	raw := strings.TrimRight(cfg.BaseURL, "/")
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	base, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("c8y: parse base URL: %w", err)
	}
	tenant, user := cfg.Tenant, cfg.User
	if before, after, ok := strings.Cut(user, "/"); ok {
		tenant, user = before, after
	}
	if tenant != "" {
		user = tenant + "/" + user
	}
	return &Client{
		base:   base,
		tenant: tenant,
		user:   user,
		auth:   "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+cfg.Password)),
		hc:     &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// BaseURL returns the tenant base URL without trailing slash.
func (c *Client) BaseURL() string { return c.base.String() }

type apiError struct {
	status int
	path   string
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("c8y: GET %s: %d: %s", e.path, e.status, e.body)
}

// notFoundError is a lookup that legitimately found nothing, as opposed to a
// failed request. It lets a resolver widen its search without matching on
// error strings.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }

func notFoundf(format string, args ...any) error {
	return &notFoundError{msg: fmt.Sprintf(format, args...)}
}

// NotFound reports whether err is a 404 or an empty lookup result.
func NotFound(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound {
		return true
	}
	var nf *notFoundError
	return errors.As(err, &nf)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	target := path
	if !strings.HasPrefix(target, "http") {
		target = c.base.String() + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("c8y: build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("c8y: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return &apiError{status: resp.StatusCode, path: path, body: string(body)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("c8y: decode %s: %w", path, err)
	}
	return nil
}

// ManagedObject fetches an inventory object by ID.
// withParents is required: Cumulocity leaves assetParents and deviceParents
// empty on a plain GET, and neighbour discovery walks exactly those.
func (c *Client) ManagedObject(ctx context.Context, id string) (*ManagedObject, error) {
	var mo ManagedObject
	path := "/inventory/managedObjects/" + url.PathEscape(id) + "?withParents=true"
	if err := c.get(ctx, path, &mo); err != nil {
		return nil, err
	}
	return &mo, nil
}

// ResolveDevice accepts a managed object ID, an external ID (any type, tried
// as c8y_Serial first) or a device name and returns the managed object.
func (c *Client) ResolveDevice(ctx context.Context, ref string) (*ManagedObject, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("c8y: empty device reference")
	}
	if _, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if mo, err := c.ManagedObject(ctx, ref); err == nil {
			return mo, nil
		} else if !NotFound(err) {
			return nil, err
		}
	}
	for _, idType := range []string{"c8y_Serial", "c8y_Imei"} {
		var res struct {
			ManagedObject Source `json:"managedObject"`
		}
		path := fmt.Sprintf("/identity/externalIds/%s/%s", url.PathEscape(idType), url.PathEscape(ref))
		if err := c.get(ctx, path, &res); err == nil {
			return c.ManagedObject(ctx, res.ManagedObject.ID)
		} else if !NotFound(err) {
			return nil, err
		}
	}
	// Exact name first, then a prefix match. Operators quote devices the way
	// they are labelled in the field ("Power Node 01"), while the inventory
	// name often carries an extra qualifier ("Power Node 01 (Feeder-A)"), and
	// an exact-only match would send the caller away over a suffix. A prefix
	// hit is only accepted when it is unambiguous: guessing between several
	// devices is worse than asking.
	mo, err := c.deviceByName(ctx, fmt.Sprintf("name eq '%s'", escapeQuery(ref)), ref)
	if err == nil || !NotFound(err) {
		return mo, err
	}
	return c.deviceByName(ctx, fmt.Sprintf("name eq '%s*'", escapeQuery(ref)), ref)
}

// escapeQuery strips the quote that would otherwise break out of the inventory
// query literal.
func escapeQuery(s string) string {
	return strings.ReplaceAll(s, "'", "")
}

func (c *Client) deviceByName(ctx context.Context, query, ref string) (*ManagedObject, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("pageSize", "5")
	var res struct {
		ManagedObjects []ManagedObject `json:"managedObjects"`
	}
	if err := c.get(ctx, "/inventory/managedObjects?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	// Groups and other assets can carry a matching name too; only devices can
	// be diagnosed.
	var devices []ManagedObject
	for _, mo := range res.ManagedObjects {
		if mo.IsDevice() {
			devices = append(devices, mo)
		}
	}
	switch len(devices) {
	case 0:
		return nil, notFoundf("c8y: no device found for %q", ref)
	case 1:
		// Re-fetch by ID so the parent references are populated.
		return c.ManagedObject(ctx, devices[0].ID)
	}
	names := make([]string, 0, len(devices))
	for _, d := range devices {
		names = append(names, fmt.Sprintf("%s (id %s)", d.Name, d.ID))
	}
	return nil, fmt.Errorf("c8y: %q matches several devices, name one of them exactly: %s",
		ref, strings.Join(names, ", "))
}

// ChildAssets returns the child assets of a managed object.
func (c *Client) ChildAssets(ctx context.Context, id string) ([]Source, error) {
	var res struct {
		References []Reference `json:"references"`
	}
	path := fmt.Sprintf("/inventory/managedObjects/%s/childAssets?pageSize=2000",
		url.PathEscape(id))
	if err := c.get(ctx, path, &res); err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(res.References))
	for _, r := range res.References {
		out = append(out, r.ManagedObject)
	}
	return out, nil
}

// Measurements returns all measurements of a source in [from,to], following
// pagination up to maxPoints.
func (c *Client) Measurements(ctx context.Context, source string, from, to time.Time, maxPoints int) ([]Measurement, error) {
	q := url.Values{}
	q.Set("source", source)
	q.Set("dateFrom", from.UTC().Format(time.RFC3339))
	q.Set("dateTo", to.UTC().Format(time.RFC3339))
	q.Set("pageSize", "2000")
	q.Set("revert", "false")

	var out []Measurement
	next := "/measurement/measurements?" + q.Encode()
	for next != "" && len(out) < maxPoints {
		var page struct {
			Measurements []Measurement `json:"measurements"`
			Next         string        `json:"next"`
		}
		if err := c.get(ctx, next, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Measurements...)
		if len(page.Measurements) == 0 {
			break
		}
		next = page.Next
	}
	if len(out) > maxPoints {
		out = out[:maxPoints]
	}
	return out, nil
}

// Alarms returns alarms of a source in [from,to].
func (c *Client) Alarms(ctx context.Context, source string, from, to time.Time, pageSize int) ([]Alarm, error) {
	q := url.Values{}
	q.Set("source", source)
	q.Set("dateFrom", from.UTC().Format(time.RFC3339))
	q.Set("dateTo", to.UTC().Format(time.RFC3339))
	q.Set("pageSize", strconv.Itoa(pageSize))
	q.Set("withSourceAssets", "false")
	var res struct {
		Alarms []Alarm `json:"alarms"`
	}
	if err := c.get(ctx, "/alarm/alarms?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	return res.Alarms, nil
}

// Events returns events of a source in [from,to].
func (c *Client) Events(ctx context.Context, source string, from, to time.Time, pageSize int) ([]Event, error) {
	q := url.Values{}
	q.Set("source", source)
	q.Set("dateFrom", from.UTC().Format(time.RFC3339))
	q.Set("dateTo", to.UTC().Format(time.RFC3339))
	q.Set("pageSize", strconv.Itoa(pageSize))
	var res struct {
		Events []Event `json:"events"`
	}
	if err := c.get(ctx, "/event/events?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	return res.Events, nil
}

// SmartRules returns the smart rules attached to a device, best effort: the
// microservice may not be subscribed, in which case the result is empty.
func (c *Client) SmartRules(ctx context.Context, deviceID string) ([]map[string]any, error) {
	var res struct {
		Rules []map[string]any `json:"rules"`
	}
	path := fmt.Sprintf("/service/smartrule/managedObjects/%s/smartrules?pageSize=100",
		url.PathEscape(deviceID))
	if err := c.get(ctx, path, &res); err != nil {
		return nil, err
	}
	return res.Rules, nil
}

// UploadBinary stores a file in the inventory binary repository and returns
// the ID of the created managed object.
func (c *Client) UploadBinary(ctx context.Context, name, contentType string, data []byte, extra map[string]any) (string, error) {
	object := map[string]any{
		"name": name,
		"type": contentType,
	}
	for k, v := range extra {
		object[k] = v
	}
	meta, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("c8y: marshal binary metadata: %w", err)
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("object", string(meta)); err != nil {
		return "", fmt.Errorf("c8y: write binary metadata: %w", err)
	}
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		return "", fmt.Errorf("c8y: create binary part: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("c8y: write binary: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("c8y: close binary writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base.String()+"/inventory/binaries", &body)
	if err != nil {
		return "", fmt.Errorf("c8y: build binary request: %w", err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("c8y: upload binary: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode >= 300 {
		return "", &apiError{status: resp.StatusCode, path: "/inventory/binaries", body: string(raw)}
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("c8y: decode binary response: %w", err)
	}
	return res.ID, nil
}

// BinaryURL returns the tenant URL a stored binary can be downloaded from.
func (c *Client) BinaryURL(id string) string {
	return fmt.Sprintf("%s/inventory/binaries/%s", c.PublicBaseURL(), id)
}

// PublicBaseURL is the URL a human outside the cluster can open. It differs
// from BaseURL on the microservice platform, where C8Y_BASEURL points at an
// internal address such as http://cumulocity:8111 that nobody can reach.
func (c *Client) PublicBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.publicBase != "" {
		return c.publicBase
	}
	return c.base.String()
}

// ResolvePublicBaseURL asks the platform for the tenant's external domain and
// remembers it. Failures are not fatal: the internal base URL stays in use.
func (c *Client) ResolvePublicBaseURL(ctx context.Context) (string, error) {
	var res struct {
		DomainName string `json:"domainName"`
	}
	if err := c.get(ctx, "/tenant/currentTenant", &res); err != nil {
		return c.PublicBaseURL(), err
	}
	if res.DomainName == "" {
		return c.PublicBaseURL(), errors.New("c8y: tenant reports no domain name")
	}
	public := "https://" + strings.TrimRight(res.DomainName, "/")

	c.mu.Lock()
	c.publicBase = public
	c.mu.Unlock()
	return public, nil
}
