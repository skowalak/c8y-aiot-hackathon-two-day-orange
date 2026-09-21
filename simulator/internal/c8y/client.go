// Package c8y contains a minimal Cumulocity IoT client: the subset of the REST
// API the simulator needs plus a SmartREST 2.0 MQTT device connection.
package c8y

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ctJSON                  = "application/json"
	ctMeasurementCollection = "application/vnd.com.nsn.cumulocity.measurementcollection+json"
	ctManagedObjectRef      = "application/vnd.com.nsn.cumulocity.managedObjectReference+json"

	// ExternalIDTypeSerial is the external ID type an MQTT client ID is mapped to.
	ExternalIDTypeSerial = "c8y_Serial"
)

// Config holds the tenant connection parameters.
type Config struct {
	BaseURL  string
	Tenant   string
	User     string
	Password string
}

// Client is a Cumulocity REST client using basic authentication.
type Client struct {
	base   *url.URL
	tenant string
	user   string
	pass   string
	auth   string
	hc     *http.Client
}

// New validates the configuration and returns a ready to use client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("c8y: base URL is required")
	}
	if cfg.User == "" || cfg.Password == "" {
		return nil, errors.New("c8y: user and password are required")
	}
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("c8y: parse base URL: %w", err)
	}
	if base.Scheme == "" {
		base, err = url.Parse("https://" + strings.TrimRight(cfg.BaseURL, "/"))
		if err != nil {
			return nil, fmt.Errorf("c8y: parse base URL: %w", err)
		}
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
		pass:   cfg.Password,
		auth:   "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+cfg.Password)),
		hc:     &http.Client{Timeout: 90 * time.Second},
	}, nil
}

// Host returns the tenant host, e.g. for building the MQTT broker address.
func (c *Client) Host() string { return c.base.Host }

// MQTTUser returns the fully qualified user name (tenant/user).
func (c *Client) MQTTUser() string { return c.user }

// Tenant returns the tenant ID.
func (c *Client) Tenant() string { return c.tenant }

// DeviceUser returns the user name a bulk registered device authenticates
// with: <tenant>/device_<id>.
func (c *Client) DeviceUser(externalID string) string {
	if c.tenant == "" {
		return "device_" + externalID
	}
	return c.tenant + "/device_" + externalID
}

// Password returns the configured password.
func (c *Client) Password() string { return c.pass }

type apiError struct {
	status int
	method string
	path   string
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("c8y: %s %s: %d: %s", e.method, e.path, e.status, e.body)
}

func (c *Client) do(ctx context.Context, method, path string, body any, contentType string, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("c8y: marshal %s %s: %w", method, path, err)
		}
	}
	if contentType == "" {
		contentType = ctJSON
	}

	const attempts = 4
	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			delay := time.Duration(1<<attempt) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
		if err != nil {
			return fmt.Errorf("c8y: build %s %s: %w", method, path, err)
		}
		req.Header.Set("Authorization", c.auth)
		req.Header.Set("Accept", ctJSON)
		if payload != nil {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("c8y: %s %s: %w", method, path, err)
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode < 300:
			if out != nil && len(respBody) > 0 {
				if err := json.Unmarshal(respBody, out); err != nil {
					return fmt.Errorf("c8y: decode %s %s: %w", method, path, err)
				}
			}
			return nil
		case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
			lastErr = &apiError{resp.StatusCode, method, path, string(respBody)}
		default:
			return &apiError{resp.StatusCode, method, path, string(respBody)}
		}
	}
	return lastErr
}

// NotFound reports whether err is a 404 from the API.
func NotFound(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound
}

type idResponse struct {
	ID string `json:"id"`
}

// CreateManagedObject creates an inventory object and returns its ID.
func (c *Client) CreateManagedObject(ctx context.Context, mo map[string]any) (string, error) {
	var res idResponse
	if err := c.do(ctx, http.MethodPost, "/inventory/managedObjects", mo, ctJSON, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}

// UpdateManagedObject patches an existing inventory object.
func (c *Client) UpdateManagedObject(ctx context.Context, id string, patch map[string]any) error {
	return c.do(ctx, http.MethodPut, "/inventory/managedObjects/"+id, patch, ctJSON, nil)
}

// LookupExternalID resolves an external ID to a managed object ID.
func (c *Client) LookupExternalID(ctx context.Context, idType, externalID string) (string, error) {
	var res struct {
		ManagedObject idResponse `json:"managedObject"`
	}
	path := fmt.Sprintf("/identity/externalIds/%s/%s", url.PathEscape(idType), url.PathEscape(externalID))
	if err := c.do(ctx, http.MethodGet, path, nil, "", &res); err != nil {
		return "", err
	}
	return res.ManagedObject.ID, nil
}

// BindExternalID attaches an external ID to a managed object.
func (c *Client) BindExternalID(ctx context.Context, moID, idType, externalID string) error {
	body := map[string]any{"type": idType, "externalId": externalID}
	path := fmt.Sprintf("/identity/globalIds/%s/externalIds", url.PathEscape(moID))
	return c.do(ctx, http.MethodPost, path, body, ctJSON, nil)
}

// AddChildAsset assigns a managed object as child asset, e.g. to a group.
func (c *Client) AddChildAsset(ctx context.Context, parentID, childID string) error {
	body := map[string]any{"managedObject": idResponse{ID: childID}}
	path := fmt.Sprintf("/inventory/managedObjects/%s/childAssets", url.PathEscape(parentID))
	return c.do(ctx, http.MethodPost, path, body, ctManagedObjectRef, nil)
}

// FindManagedObjectByName returns the ID of the first object matching name and type.
func (c *Client) FindManagedObjectByName(ctx context.Context, name, moType string) (string, error) {
	q := url.Values{}
	query := fmt.Sprintf("name eq '%s'", name)
	if moType != "" {
		query += fmt.Sprintf(" and type eq '%s'", moType)
	}
	q.Set("query", query)
	q.Set("pageSize", "1")
	var res struct {
		ManagedObjects []idResponse `json:"managedObjects"`
	}
	if err := c.do(ctx, http.MethodGet, "/inventory/managedObjects?"+q.Encode(), nil, "", &res); err != nil {
		return "", err
	}
	if len(res.ManagedObjects) == 0 {
		return "", nil
	}
	return res.ManagedObjects[0].ID, nil
}

// CreateMeasurements bulk creates measurements in a single request.
func (c *Client) CreateMeasurements(ctx context.Context, ms []Measurement) error {
	if len(ms) == 0 {
		return nil
	}
	body := map[string]any{"measurements": ms}
	return c.do(ctx, http.MethodPost, "/measurement/measurements", body, ctMeasurementCollection, nil)
}

// CreateAlarm raises an alarm and returns its ID.
func (c *Client) CreateAlarm(ctx context.Context, a Alarm) (string, error) {
	var res idResponse
	if err := c.do(ctx, http.MethodPost, "/alarm/alarms", a, ctJSON, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}

// UpdateAlarmStatus sets the status of an existing alarm (ACKNOWLEDGED, CLEARED).
func (c *Client) UpdateAlarmStatus(ctx context.Context, id, status string) error {
	return c.do(ctx, http.MethodPut, "/alarm/alarms/"+url.PathEscape(id),
		map[string]any{"status": status}, ctJSON, nil)
}

// CreateEvent stores an event.
func (c *Client) CreateEvent(ctx context.Context, e Event) (string, error) {
	var res idResponse
	if err := c.do(ctx, http.MethodPost, "/event/events", e, ctJSON, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}
