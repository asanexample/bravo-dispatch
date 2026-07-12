// Package shipmentsclient is the typed client for the internal shipments service, used by every service that
// calls it east-west (tracker's BFF lookups; intake's create; dispatch-worker's status updates). Every call
// uses an otelhttp-wrapped transport (via internal/telemetry), so a caller→shipments request propagates the
// W3C trace context and shows up as one connected distributed trace with a shipments child span.
package shipmentsclient

import (
	"bytes"
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

// CreateShipmentRequest is intake's input for creating a new shipment.
type CreateShipmentRequest struct {
	Recipient   string `json:"recipient"`
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
}

// CreateShipment asks shipments to allocate + persist a new shipment and returns it (with its generated
// tracking id).
func (c *Client) CreateShipment(ctx context.Context, req CreateShipmentRequest) (shipments.Shipment, error) {
	var out shipments.Shipment
	err := c.post(ctx, "/shipments", http.StatusCreated, req, &out)
	return out, err
}

// UpdateStatusRequest is dispatch-worker's input for advancing a shipment's status.
type UpdateStatusRequest = shipments.StatusUpdate

// UpdateStatus asks shipments to append a timeline event and advance the shipment's current
// Status/CurrentLocation/ETA (and Carrier, once assigned). ok=false when id is unknown (404).
func (c *Client) UpdateStatus(ctx context.Context, id string, req UpdateStatusRequest) (shipments.Shipment, bool, error) {
	var out shipments.Shipment
	err := c.post(ctx, "/shipments/"+id+"/status", http.StatusOK, req, &out)
	if err == errNotFound {
		return out, false, nil
	}
	return out, err == nil, err
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

// post sends a JSON body and decodes a JSON response, treating wantStatus as the only success code (404 maps
// to errNotFound so callers can distinguish "not found" from a transport/upstream failure).
func (c *Client) post(ctx context.Context, path string, wantStatus int, body, dst any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("shipments request %s: encode: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("shipments request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != wantStatus {
		return fmt.Errorf("shipments %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("shipments %s decode: %w", path, err)
	}
	return nil
}
