// Command tracker is the Bravo Dispatch tracker (team bravo, product dispatch, service tracker).
//
// It is Bravo Dispatch's front door: the ONLY edge-exposed service (its HTTPRoute serves the public host). It
// does two jobs in one Kyverno-compliant image (the flagship pattern): it serves the embedded React SPA, and
// it runs a BFF API the SPA calls, which looks up parcels via the internal shipments service over an
// east-west call. That call propagates the trace (internal/telemetry), so a page load is one connected trace
// spanning tracker→shipments.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asanexample/bravo-dispatch/internal/shipmentsclient"
	"github.com/asanexample/bravo-dispatch/internal/telemetry"
	"github.com/asanexample/bravo-dispatch/web"
)

type server struct {
	shipments *shipmentsclient.Client
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	log := telemetry.Logger

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Sample shipments for the landing page's demo tracking-number links.
	mux.HandleFunc("GET /api/shipments", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.shipments.Shipments(r.Context())
		if err != nil {
			s.fail(w, r, "shipments list", err)
			return
		}
		log.InfoContext(r.Context(), "tracker sample list", "count", len(list))
		writeJSON(w, http.StatusOK, map[string]any{"shipments": list, "count": len(list)})
	})

	// Shipment detail + timeline, keyed by tracking number.
	mux.HandleFunc("GET /api/shipments/{id}", func(w http.ResponseWriter, r *http.Request) {
		sh, ok, err := s.shipments.Shipment(r.Context(), r.PathValue("id"))
		if err != nil {
			s.fail(w, r, "shipment detail", err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "shipment not found"})
			return
		}
		writeJSON(w, http.StatusOK, sh)
	})

	// The SPA (embedded under -tags tracker). "/" is the catch-all; the /api and /healthz patterns above
	// are more specific, so they win. Absent (plain build) → API only.
	if h := web.Handler(); h != nil {
		mux.Handle("/", h)
		log.Info("tracker UI: embedded")
	} else {
		log.Info("tracker UI: not embedded (build with -tags tracker)")
	}

	return mux
}

func (s *server) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	// A failed east-west call must not blank the page hard; log it (with trace_id) and return a clean 502.
	telemetry.Logger.ErrorContext(r.Context(), "bff upstream failed", "what", what, "err", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream unavailable: " + what})
}

func main() {
	ctx := context.Background()
	shutdown, err := telemetry.Setup(ctx, "dispatch-tracker")
	if err != nil {
		telemetry.Logger.Error("otel init failed; continuing without tracing", "err", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	shipmentsURL := getenv("SHIPMENTS_URL", "http://shipments")
	srv := &server{shipments: shipmentsclient.New(shipmentsURL)}
	handler := telemetry.WrapHandler(chaosMiddleware(srv.routes()), "http.server")

	httpSrv := &http.Server{Addr: getenv("ADDR", ":8080"), Handler: handler, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}

	go func() {
		telemetry.Logger.Info("starting dispatch-tracker", "addr", httpSrv.Addr, "shipments", shipmentsURL)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			telemetry.Logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	telemetry.Logger.Info("shutting down (draining in-flight requests)…")
	ctxShutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctxShutdown); err != nil {
		telemetry.Logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	telemetry.Logger.Info("stopped")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
