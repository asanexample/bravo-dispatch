// Package securerand provides crypto/rand-backed replacements for the couple of math/rand call shapes used
// elsewhere in this repo (a carrier-selector index, a tracking-number suffix). Neither use is actually
// security-sensitive — a simulated carrier pick and a demo tracking number — but crypto/rand costs nothing
// here and satisfies the SAST audit rule (go.lang.security.audit.crypto.math_random) outright rather than
// arguing the rule doesn't apply.
package securerand

import (
	"crypto/rand"
	"math/big"
)

// Intn returns a non-negative pseudo-random int in [0,n), matching math/rand.Intn's call shape and
// contract (panics if n <= 0). Falls back to 0 only if the OS entropy source itself is unavailable —
// crypto/rand.Int erroring here would mean a broken system, not a normal runtime condition.
func Intn(n int) int {
	if n <= 0 {
		panic("securerand: Intn called with n <= 0")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// Int returns a non-negative pseudo-random int, matching math/rand.Int's call shape (func() int) so it can
// be passed directly wherever that signature is expected.
func Int() int {
	return Intn(1<<31 - 1) // stays within int32 range regardless of platform int size
}
