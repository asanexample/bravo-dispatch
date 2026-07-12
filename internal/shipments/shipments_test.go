package shipments

import (
	"context"
	"testing"
	"time"

	"github.com/asanexample/bravo-dispatch/internal/awskv"
)

func fixedNow() time.Time { return time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC) }

func TestSeedPopulatesAllSampleIDs(t *testing.T) {
	ctx := context.Background()
	store := New(awskv.NewMemory())

	seeded, err := Seed(ctx, store, fixedNow)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(seeded) != len(SampleIDs) {
		t.Fatalf("Seed reported %d newly-seeded ids, want %d", len(seeded), len(SampleIDs))
	}

	got, err := List(ctx, store)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(SampleIDs) {
		t.Fatalf("List returned %d shipments, want %d", len(got), len(SampleIDs))
	}
	for i, id := range SampleIDs {
		if got[i].ID != id {
			t.Errorf("List()[%d].ID = %q, want %q (order should match SampleIDs)", i, got[i].ID, id)
		}
		if len(got[i].Timeline) == 0 {
			t.Errorf("List()[%d] (%s) has an empty timeline", i, id)
		}
	}
}

// TestSeedIsIdempotent proves the "upsert-if-absent" contract: seeding twice must not clobber a shipment
// that's since been mutated (e.g. a status update from a later phase) with the fabricated sample again.
func TestSeedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := New(awskv.NewMemory())

	if _, err := Seed(ctx, store, fixedNow); err != nil {
		t.Fatalf("first Seed: %v", err)
	}

	// Mutate one shipment as if it progressed past its seeded state.
	sh, found, err := store.Get(ctx, "BD-10023")
	if err != nil || !found {
		t.Fatalf("Get(BD-10023) = %v, %v, %v", sh, found, err)
	}
	sh.Status = Delivered
	sh.CurrentLocation = "Delivered — front porch"
	if err := store.Save(ctx, sh); err != nil {
		t.Fatalf("Save mutated shipment: %v", err)
	}

	seededAgain, err := Seed(ctx, store, fixedNow)
	if err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if len(seededAgain) != 0 {
		t.Fatalf("second Seed re-seeded %v, want none (already-present ids must be left alone)", seededAgain)
	}

	after, found, err := store.Get(ctx, "BD-10023")
	if err != nil || !found {
		t.Fatalf("Get(BD-10023) after re-seed = %v, %v, %v", after, found, err)
	}
	if after.Status != Delivered {
		t.Errorf("re-seed clobbered the mutated shipment: status = %q, want %q", after.Status, Delivered)
	}
}

func TestGetUnknownIDNotFound(t *testing.T) {
	store := New(awskv.NewMemory())
	_, found, err := store.Get(context.Background(), "BD-99999")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatalf("Get(unknown id) found = true, want false")
	}
}

// fixedInt returns a randInt func that always returns n — deterministic id allocation for tests.
func fixedInt(n int) func() int { return func() int { return n } }

func TestCreateAllocatesIDAndSeedsTimeline(t *testing.T) {
	ctx := context.Background()
	store := New(awskv.NewMemory())

	req := CreateRequest{Recipient: "Dana Okafor", Origin: "Test Origin Depot", Destination: "Test Destination"}
	sh, err := Create(ctx, store, req, fixedNow, fixedInt(23456))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sh.ID != "BD-33456" {
		t.Errorf("Create id = %q, want BD-33456 (10000 + 23456%%90000)", sh.ID)
	}
	if sh.Status != Created {
		t.Errorf("Create status = %q, want %q", sh.Status, Created)
	}
	if sh.CurrentLocation != req.Origin {
		t.Errorf("Create CurrentLocation = %q, want origin %q", sh.CurrentLocation, req.Origin)
	}
	if len(sh.Timeline) != 1 || sh.Timeline[0].Status != Created {
		t.Fatalf("Create timeline = %+v, want exactly one Created event", sh.Timeline)
	}

	// Persisted, not just returned.
	stored, found, err := store.Get(ctx, sh.ID)
	if err != nil || !found {
		t.Fatalf("Get(%s) after Create = %v, %v, %v", sh.ID, stored, found, err)
	}
	if stored.Recipient != req.Recipient {
		t.Errorf("stored Recipient = %q, want %q", stored.Recipient, req.Recipient)
	}
}

func TestCreateRetriesOnIDCollision(t *testing.T) {
	ctx := context.Background()
	store := New(awskv.NewMemory())

	// Pre-occupy the id that fixedInt(1) would allocate on the first attempt.
	if err := store.Save(ctx, Shipment{ID: "BD-10001"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	calls := 0
	pick := func() int {
		calls++
		if calls == 1 {
			return 1 // collides with BD-10001
		}
		return 2 // BD-10002, free
	}
	sh, err := Create(ctx, store, CreateRequest{Recipient: "R", Origin: "O", Destination: "D"}, fixedNow, pick)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sh.ID != "BD-10002" {
		t.Errorf("Create id after collision = %q, want BD-10002", sh.ID)
	}
	if calls < 2 {
		t.Errorf("randInt called %d times, want at least 2 (collision then retry)", calls)
	}
}

func TestUpdateStatusAppendsEventAndAdvancesFields(t *testing.T) {
	ctx := context.Background()
	store := New(awskv.NewMemory())
	created, err := Create(ctx, store, CreateRequest{Recipient: "R", Origin: "Origin City", Destination: "D"}, fixedNow, fixedInt(1))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	eta := fixedNow().Add(4 * time.Hour)
	upd := StatusUpdate{Status: PickedUp, Label: "Picked up — assigned to Bravo Dispatch Priority Air", CurrentLocation: "Origin City sort facility", ETA: eta, Carrier: "Bravo Dispatch Priority Air"}
	updated, found, err := UpdateStatus(ctx, store, created.ID, upd, fixedNow)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if !found {
		t.Fatalf("UpdateStatus found = false, want true")
	}
	if updated.Status != PickedUp {
		t.Errorf("Status = %q, want %q", updated.Status, PickedUp)
	}
	if updated.CurrentLocation != upd.CurrentLocation {
		t.Errorf("CurrentLocation = %q, want %q", updated.CurrentLocation, upd.CurrentLocation)
	}
	if !updated.ETA.Equal(eta) {
		t.Errorf("ETA = %v, want %v", updated.ETA, eta)
	}
	if updated.Carrier != upd.Carrier {
		t.Errorf("Carrier = %q, want %q", updated.Carrier, upd.Carrier)
	}
	if len(updated.Timeline) != 2 || updated.Timeline[1].Status != PickedUp {
		t.Fatalf("Timeline = %+v, want [Created, PickedUp]", updated.Timeline)
	}

	// Persisted, not just returned.
	stored, found, err := store.Get(ctx, created.ID)
	if err != nil || !found {
		t.Fatalf("Get after UpdateStatus = %v, %v, %v", stored, found, err)
	}
	if stored.Status != PickedUp {
		t.Errorf("persisted Status = %q, want %q", stored.Status, PickedUp)
	}
}

func TestUpdateStatusUnknownIDNotFound(t *testing.T) {
	store := New(awskv.NewMemory())
	_, found, err := UpdateStatus(context.Background(), store, "BD-99999", StatusUpdate{Status: PickedUp}, fixedNow)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if found {
		t.Fatalf("UpdateStatus(unknown id) found = true, want false")
	}
}
