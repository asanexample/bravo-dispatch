// Package shipments is the shipment domain + store for Bravo Dispatch. A shipment is a JSON document keyed by
// its tracking number, persisted via internal/awskv (DynamoDB in-cluster, memory locally). The shipments
// SERVICE (cmd/shipments) does the HTTP wiring; this package holds just the model, the store, and the demo
// seed data.
package shipments

import (
	"context"
	"encoding/json"
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
