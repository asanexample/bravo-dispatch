// Package shipmentsclient is the tracker BFF's typed client for the internal shipments service. Every call
// uses an otelhttp-wrapped transport (via internal/telemetry), so a tracker→shipments request propagates the
// W3C trace context and shows up as one connected distributed trace with a shipments child span.
package shipmentsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/asanexample/bravo-dispatch/internal/shipments"
	"github.com/asanexample/bravo-dispatch/internal/telemetry"
)

// Client talks to the shipments service over HTTP.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for the shipments service at baseURL (e.g. http://shipments).
func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: telemetry.Client()}
}

// Shipment returns a single shipment by tracking number. ok=false when not found (404).
func (c *Client) Shipment(ctx context.Context, id string) (shipments.Shipment, bool, error) {
	var out shipments.Shipment
	err := c.get(ctx, "/shipments/"+id, &out)
	if err == errNotFound {
		return out, false, nil
	}
	return out, err == nil, err
}

// Shipments returns the demo sample shipments (for the tracker landing page's sample links).
func (c *Client) Shipments(ctx context.Context) ([]shipments.Shipment, error) {
	var out struct {
		Shipments []shipments.Shipment `json:"shipments"`
	}
	return out.Shipments, c.get(ctx, "/shipments", &out)
}

var errNotFound = fmt.Errorf("not found")

func (c *Client) get(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("shipments request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("shipments %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("shipments %s decode: %w", path, err)
	}
	return nil
}
