package main

import (
	"net/http"
	"os"
	"time"
)

// chaosMiddleware, when CHAOS_INJECTION_ENABLED=true, honors an inbound X-Chaos-Fault header from a
// trusted internal caller (Pulse, the platform's synthetic-traffic generator — never a real user) to
// inject a real fault: "500" returns an immediate 500, "timeout" sleeps a few seconds before responding
// (feeding the latency SLO leg too). Inert by default — CHAOS_INJECTION_ENABLED is set ONLY in the dev
// overlay's env (see k8s/overlays/dev), never in prod.
//
// Wire this as telemetry.WrapHandler(chaosMiddleware(srv.routes()), "http.server") — otelhttp must stay
// OUTERMOST. Its response-writer wrapper captures whatever status code actually got written, so a chaos
// short-circuit still lands as a real 5xx on the span + RED metric, even though it never reaches routes().
func chaosMiddleware(next http.Handler) http.Handler {
	if os.Getenv("CHAOS_INJECTION_ENABLED") != "true" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Chaos-Fault") {
		case "500":
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "simulated fault injected by chaos header"})
			return
		case "timeout":
			time.Sleep(5 * time.Second)
		}
		next.ServeHTTP(w, r)
	})
}
