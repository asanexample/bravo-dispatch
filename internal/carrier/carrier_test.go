package carrier

import (
	"testing"
	"time"
)

func TestAssignPriorityAlwaysFastLane(t *testing.T) {
	// pick should never be consulted on the priority path — fail the test if it is.
	pick := func(n int) int { t.Fatalf("pick called on the priority path (n=%d)", n); return 0 }

	got := Assign(true, pick)
	if got.Carrier != "Bravo Dispatch Priority Air" {
		t.Errorf("Assign(true) carrier = %q, want the priority fast lane", got.Carrier)
	}
	if got.ETA >= 14*time.Hour {
		t.Errorf("Assign(true) ETA = %v, want shorter than the normal-lane ETAs", got.ETA)
	}
}

func TestAssignNormalUsesPickFunc(t *testing.T) {
	tests := []struct {
		pickReturn  int
		wantCarrier string
	}{
		{0, "Bravo Dispatch Ground"},
		{1, "Bravo Dispatch Air"},
	}
	for _, tt := range tests {
		got := Assign(false, func(int) int { return tt.pickReturn })
		if got.Carrier != tt.wantCarrier {
			t.Errorf("Assign(false, pick->%d) carrier = %q, want %q", tt.pickReturn, got.Carrier, tt.wantCarrier)
		}
	}
}

func TestAssignNormalNeverReturnsThePriorityCarrier(t *testing.T) {
	for i := 0; i < 2; i++ {
		got := Assign(false, func(int) int { return i })
		if got.Carrier == "Bravo Dispatch Priority Air" {
			t.Errorf("Assign(false) returned the priority carrier at pick=%d", i)
		}
	}
}
