// Package carrier picks a simulated carrier + ETA for a shipment once dispatch-worker pulls it off the
// requests queue. It is deliberately small and deterministic-when-tested: the routing "logic" is a demo stand
// -in, not a real carrier-rating engine, but it is real code with a real branch (the enable-priority-routing
// flag) worth unit testing on its own, independent of SQS/HTTP.
package carrier

import "time"

// Assignment is the carrier + ETA-from-now dispatch-worker assigns to a shipment.
type Assignment struct {
	Carrier string
	ETA     time.Duration
}

// normal are the non-priority carrier/ETA choices; pick selects among them.
var normal = []Assignment{
	{Carrier: "Bravo Dispatch Ground", ETA: 30 * time.Hour},
	{Carrier: "Bravo Dispatch Air", ETA: 14 * time.Hour},
}

// priority is the fast lane used when the enable-priority-routing flag evaluates true.
var priority = Assignment{Carrier: "Bravo Dispatch Priority Air", ETA: 4 * time.Hour}

// Assign picks a carrier/route for a shipment. When usePriority is true (the enable-priority-routing flag) it
// always returns the fast lane; otherwise it picks among the normal carriers using pick(n) — a
// caller-supplied selector in [0,n) so tests get a deterministic choice and production can pass
// math/rand.Intn.
func Assign(usePriority bool, pick func(n int) int) Assignment {
	if usePriority {
		return priority
	}
	return normal[pick(len(normal))]
}
