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
