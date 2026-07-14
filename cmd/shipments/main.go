// Command shipments is the Bravo Dispatch shipments service (team bravo, product dispatch, service shipments).
//
// Internal, east-west only (ClusterIP, no HTTPRoute): the tracker BFF calls it to look up a parcel's status
// and timeline; intake calls it to create a new shipment; dispatch-worker calls it to advance a shipment's
// status once it's picked a carrier. Backed by the self-service DynamoDB table (SHIPMENTS_TABLE, ADR-073),
// falling back to memory when unset (local dev / tests). On startup it seeds a small set of obviously-fictional
// demo shipments (internal/shipments.Seed) so the tracker's landing page always has something to link to.
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

	"github.com/asanexample/bravo-dispatch/internal/awskv"
	"github.com/asanexample/bravo-dispatch/internal/securerand"
	"github.com/asanexample/bravo-dispatch/internal/shipments"
	"github.com/asanexample/bravo-dispatch/internal/telemetry"
)

type server struct {
	store *shipments.Store
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	log := telemetry.Logger

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /shipments", func(w http.ResponseWriter, r *http.Request) {
		list, err := shipments.List(r.Context(), s.store)
		if err != nil {
			log.ErrorContext(r.Context(), "shipments list failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"shipments": list, "count": len(list)})
	})

	mux.HandleFunc("GET /shipments/{id}", func(w http.ResponseWriter, r *http.Request) {
		sh, found, err := s.store.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			log.ErrorContext(r.Context(), "shipment get failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "shipment not found"})
			return
		}
		writeJSON(w, http.StatusOK, sh)
	})

	// Called by intake: create a brand-new shipment (status starts at Created, one timeline event).
	mux.HandleFunc("POST /shipments", func(w http.ResponseWriter, r *http.Request) {
		var req shipments.CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		sh, err := shipments.Create(r.Context(), s.store, req, func() time.Time { return time.Now().UTC() }, securerand.Int)
		if err != nil {
			log.ErrorContext(r.Context(), "shipment create failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		log.InfoContext(r.Context(), "shipment created", "id", sh.ID)
		writeJSON(w, http.StatusCreated, sh)
	})

	// Called by dispatch-worker: append a timeline event and advance the shipment's current status/location/ETA.
	mux.HandleFunc("POST /shipments/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		var req shipments.StatusUpdate
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		sh, found, err := shipments.UpdateStatus(r.Context(), s.store, r.PathValue("id"), req, func() time.Time { return time.Now().UTC() })
		if err != nil {
			log.ErrorContext(r.Context(), "shipment status update failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "shipment not found"})
			return
		}
		log.InfoContext(r.Context(), "shipment status updated", "id", sh.ID, "status", sh.Status)
		writeJSON(w, http.StatusOK, sh)
	})

	return mux
}

func main() {
	ctx := context.Background()
	shutdown, err := telemetry.Setup(ctx, "dispatch-shipments")
	if err != nil {
		telemetry.Logger.Error("otel init failed; continuing without tracing", "err", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	kv, err := awskv.Open(ctx, os.Getenv("SHIPMENTS_TABLE"))
	if err != nil {
		telemetry.Logger.Error("kv init failed", "err", err)
		os.Exit(1)
	}
	srv := &server{store: shipments.New(kv)}

	seeded, err := shipments.Seed(ctx, srv.store, func() time.Time { return time.Now().UTC() })
	if err != nil {
		// Best-effort: a seed failure (e.g. a transient DynamoDB error) must not stop the service serving
		// whatever data does exist.
		telemetry.Logger.Error("shipment seed failed; continuing", "err", err)
	}
	telemetry.Logger.Info("shipments backend", "store", srv.store.Backend(), "seeded", seeded)

	httpSrv := &http.Server{Addr: getenv("ADDR", ":8080"), Handler: telemetry.WrapHandler(srv.routes(), "http.server"), ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}

	go func() {
		telemetry.Logger.Info("starting dispatch-shipments", "addr", httpSrv.Addr)
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
