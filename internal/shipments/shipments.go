// Package shipments is the shipment domain + store for Bravo Dispatch. A shipment is a JSON document keyed by
// its tracking number, persisted via internal/awskv (DynamoDB in-cluster, memory locally). The shipments
// SERVICE (cmd/shipments) does the HTTP wiring; this package holds just the model, the store, and the demo
// seed data.
package shipments

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/asanexample/bravo-dispatch/internal/awskv"
)

// Status is a point in a shipment's delivery lifecycle.
type Status string

const (
	Created        Status = "created"
	PickedUp       Status = "picked_up"
	InTransit      Status = "in_transit"
	OutForDelivery Status = "out_for_delivery"
	Delivered      Status = "delivered"
)

// Event is one entry in a shipment's status timeline.
type Event struct {
	Status Status    `json:"status"`
	Label  string    `json:"label"`
	At     time.Time `json:"at"`
}

// Shipment is a last-mile parcel and its delivery timeline. All data here is fictional demo data — no real
// customer PII (see the platform's PII-in-git boundary).
type Shipment struct {
	ID              string    `json:"id"` // the tracking number, e.g. "BD-10023"
	Recipient       string    `json:"recipient"`
	Origin          string    `json:"origin"`
	Destination     string    `json:"destination"`
	Carrier         string    `json:"carrier"`
	Status          Status    `json:"status"`
	CurrentLocation string    `json:"currentLocation"`
	ETA             time.Time `json:"eta"`
	Timeline        []Event   `json:"timeline"`
}

// Store persists shipments by tracking number.
type Store struct{ kv awskv.Store }

// New returns a shipment Store over the given key-value backend.
func New(kv awskv.Store) *Store { return &Store{kv: kv} }

// Backend reports the underlying kv backend (for startup logging).
func (s *Store) Backend() string { return s.kv.Backend() }

// Save persists a shipment.
func (s *Store) Save(ctx context.Context, sh Shipment) error {
	b, err := json.Marshal(sh)
	if err != nil {
		return err
	}
	return s.kv.Put(ctx, sh.ID, b)
}

// Get returns a shipment by tracking number (found=false if unknown).
func (s *Store) Get(ctx context.Context, id string) (Shipment, bool, error) {
	doc, found, err := s.kv.Get(ctx, id)
	if err != nil || !found {
		return Shipment{}, found, err
	}
	var sh Shipment
	if err := json.Unmarshal(doc, &sh); err != nil {
		return Shipment{}, false, err
	}
	return sh, true, nil
}

// CreateRequest is the caller-supplied input to Create (the intake service's POST /shipments body).
type CreateRequest struct {
	Recipient   string `json:"recipient"`
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
}

// Create allocates a fresh tracking id (see NewID), seeds the shipment at Created with one timeline event,
// and persists it. now/randInt are injected so tests get a deterministic clock and id.
func Create(ctx context.Context, store *Store, req CreateRequest, now func() time.Time, randInt func() int) (Shipment, error) {
	id, err := NewID(ctx, store, randInt)
	if err != nil {
		return Shipment{}, err
	}
	t := now()
	sh := Shipment{
		ID:              id,
		Recipient:       req.Recipient,
		Origin:          req.Origin,
		Destination:     req.Destination,
		Carrier:         "",
		Status:          Created,
		CurrentLocation: req.Origin,
		ETA:             t.Add(72 * time.Hour), // demo placeholder ETA until dispatch-worker assigns a real one
		Timeline:        []Event{{Status: Created, Label: "Label created", At: t}},
	}
	if err := store.Save(ctx, sh); err != nil {
		return Shipment{}, err
	}
	return sh, nil
}

// StatusUpdate is the caller-supplied input to UpdateStatus (dispatch-worker's POST /shipments/{id}/status
// body): a new point in the shipment's timeline plus its current snapshot fields.
type StatusUpdate struct {
	Status          Status    `json:"status"`
	Label           string    `json:"label"`
	CurrentLocation string    `json:"currentLocation"`
	ETA             time.Time `json:"eta"`
	Carrier         string    `json:"carrier,omitempty"`
}

// UpdateStatus appends a timeline event and advances the shipment's current Status/CurrentLocation/ETA (and
// Carrier, once assigned). found=false if id is unknown — the caller decides whether that's a 404 (HTTP) or a
// drop-the-message case (the background worker).
func UpdateStatus(ctx context.Context, store *Store, id string, upd StatusUpdate, now func() time.Time) (Shipment, bool, error) {
	sh, found, err := store.Get(ctx, id)
	if err != nil || !found {
		return Shipment{}, found, err
	}
	sh.Status = upd.Status
	sh.CurrentLocation = upd.CurrentLocation
	sh.ETA = upd.ETA
	if upd.Carrier != "" {
		sh.Carrier = upd.Carrier
	}
	sh.Timeline = append(sh.Timeline, Event{Status: upd.Status, Label: upd.Label, At: now()})
	if err := store.Save(ctx, sh); err != nil {
		return Shipment{}, true, err
	}
	return sh, true, nil
}

// NewID generates a fresh tracking number in the existing "BD-#####" convention (see SampleIDs), retrying a
// few times against a collision (checked via store.Get) before giving up. The collision space (90,000 ids) is
// tiny by production standards, but a demo doesn't need a real distributed id allocator.
func NewID(ctx context.Context, store *Store, randInt func() int) (string, error) {
	const attempts = 10
	for i := 0; i < attempts; i++ {
		id := fmt.Sprintf("BD-%05d", 10000+randInt()%90000)
		_, found, err := store.Get(ctx, id)
		if err != nil {
			return "", err
		}
		if !found {
			return id, nil
		}
	}
	return "", fmt.Errorf("shipments: could not allocate a unique tracking id after %d attempts", attempts)
}

// SampleIDs are the fixed demo tracking numbers seeded on startup (see Seed) and served by List. awskv's
// contract is get/put by id only — no Scan/Query — so "list all shipments" isn't a general DynamoDB
// operation here; a fixed, known id set is the simplest correct way to serve a small demo list against
// either backend.
var SampleIDs = []string{"BD-10023", "BD-10041", "BD-10077"}

// Seed idempotently upserts the demo shipments for any SampleIDs entry not already present. It is safe to
// call on every startup and from multiple replicas concurrently: each id is fetched first and only written if
// absent, and a duplicate concurrent write is harmless (identical fabricated data, last-write-wins). Against
// DynamoDB this costs one Get + at most one Put per sample id at boot — negligible for a 3-row demo table;
// against the in-memory backend it's how the table gets its only data, since memory always starts empty.
func Seed(ctx context.Context, store *Store, now func() time.Time) (seeded []string, err error) {
	for _, sample := range sampleData(now) {
		_, found, err := store.Get(ctx, sample.ID)
		if err != nil {
			return seeded, err
		}
		if found {
			continue
		}
		if err := store.Save(ctx, sample); err != nil {
			return seeded, err
		}
		seeded = append(seeded, sample.ID)
	}
	return seeded, nil
}

// List returns every SampleIDs shipment that exists in the store (in SampleIDs order), for the tracker's
// landing-page sample links. See the SampleIDs doc for why this isn't a general list/scan.
func List(ctx context.Context, store *Store) ([]Shipment, error) {
	out := make([]Shipment, 0, len(SampleIDs))
	for _, id := range SampleIDs {
		sh, found, err := store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, sh)
		}
	}
	return out, nil
}

// sampleData builds the fixed, obviously-fictional demo shipments. now is injected so tests get a
// deterministic clock.
func sampleData(now func() time.Time) []Shipment {
	t := now()
	return []Shipment{
		{
			ID:              "BD-10023",
			Recipient:       "Priya Nakamura",
			Origin:          "Springfield Sort Center",
			Destination:     "Rivertown, DE",
			Carrier:         "Bravo Dispatch Ground",
			Status:          OutForDelivery,
			CurrentLocation: "Rivertown Local Depot",
			ETA:             t.Add(3 * time.Hour),
			Timeline: []Event{
				{Status: Created, Label: "Label created", At: t.Add(-48 * time.Hour)},
				{Status: PickedUp, Label: "Picked up from sender", At: t.Add(-40 * time.Hour)},
				{Status: InTransit, Label: "In transit", At: t.Add(-20 * time.Hour)},
				{Status: OutForDelivery, Label: "Out for delivery", At: t.Add(-1 * time.Hour)},
			},
		},
		{
			ID:              "BD-10041",
			Recipient:       "Marcus Webb",
			Origin:          "Lakeside Sort Center",
			Destination:     "Harborview, OR",
			Carrier:         "Bravo Dispatch Air",
			Status:          InTransit,
			CurrentLocation: "Harborview Regional Hub",
			ETA:             t.Add(26 * time.Hour),
			Timeline: []Event{
				{Status: Created, Label: "Label created", At: t.Add(-30 * time.Hour)},
				{Status: PickedUp, Label: "Picked up from sender", At: t.Add(-24 * time.Hour)},
				{Status: InTransit, Label: "In transit", At: t.Add(-6 * time.Hour)},
			},
		},
		{
			ID:              "BD-10077",
			Recipient:       "Elena Castillo",
			Origin:          "Fairview Sort Center",
			Destination:     "Fairview, TX",
			Carrier:         "Bravo Dispatch Ground",
			Status:          Delivered,
			CurrentLocation: "Delivered — front porch",
			ETA:             t.Add(-2 * time.Hour),
			Timeline: []Event{
				{Status: Created, Label: "Label created", At: t.Add(-72 * time.Hour)},
				{Status: PickedUp, Label: "Picked up from sender", At: t.Add(-68 * time.Hour)},
				{Status: InTransit, Label: "In transit", At: t.Add(-50 * time.Hour)},
				{Status: OutForDelivery, Label: "Out for delivery", At: t.Add(-4 * time.Hour)},
				{Status: Delivered, Label: "Delivered", At: t.Add(-2 * time.Hour)},
			},
		},
	}
}
