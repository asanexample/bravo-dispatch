// Command intake is Bravo Dispatch's shipment-intake orchestrator (team bravo, product dispatch, service
// intake).
//
// Internal only — no HTTPRoute, no AWS resources of its own (it shares the app-bravo ServiceAccount). It is a
// thin BFF-shaped front door for creating a shipment: it validates the request body, asks the shipments
// service to allocate + persist the new record (east-west), asks dispatch-worker to route it (east-west), and
// returns the created shipment to its own caller. Modeled on cmd/tracker's BFF orchestrator shape, minus the
// SPA embedding (intake has no UI). Every east-west call uses internal/telemetry's trace-propagating client,
// so an intake request is one connected trace spanning intake→shipments and intake→dispatch-worker.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/asanexample/bravo-dispatch/internal/shipmentsclient"
	"github.com/asanexample/bravo-dispatch/internal/telemetry"
)

type server struct {
	shipments   *shipmentsclient.Client
	dispatchURL string
	http        *http.Client
}

// createShipmentRequest is the public request body for POST /shipments.
type createShipmentRequest struct {
	Recipient   string `json:"recipient"`
	Origin      string `json:"origin"`
	Destination string `json:"destination"`
}

func (r createShipmentRequest) validate() error {
	switch {
	case strings.TrimSpace(r.Recipient) == "":
		return errors.New("recipient is required")
	case strings.TrimSpace(r.Origin) == "":
		return errors.New("origin is required")
	case strings.TrimSpace(r.Destination) == "":
		return errors.New("destination is required")
	}
	return nil
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	log := telemetry.Logger

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("POST /shipments", func(w http.ResponseWriter, r *http.Request) {
		var req createShipmentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if err := req.validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		sh, err := s.shipments.CreateShipment(r.Context(), shipmentsclient.CreateShipmentRequest{
			Recipient: req.Recipient, Origin: req.Origin, Destination: req.Destination,
		})
		if err != nil {
			s.fail(w, r, "shipment create", err)
			return
		}

		if err := s.dispatch(r.Context(), sh.ID); err != nil {
			// The shipment DID get created; a failed dispatch-request is logged but not fatal to the
			// caller — this demo has no retry queue of its own here, so a lost dispatch just means the
			// shipment sits at Created until manually re-dispatched. Worth a platform-side alert in a
			// non-demo product, not worth blocking shipment creation over.
			log.ErrorContext(r.Context(), "dispatch request failed", "shipmentId", sh.ID, "err", err)
		}

		log.InfoContext(r.Context(), "shipment intake complete", "shipmentId", sh.ID)
		writeJSON(w, http.StatusCreated, sh)
	})

	return mux
}

// dispatch asks dispatch-worker to route the newly created shipment (east-west, same namespace).
func (s *server) dispatch(ctx context.Context, shipmentID string) error {
	body, err := json.Marshal(map[string]string{"shipmentId": shipmentID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.dispatchURL+"/dispatch", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return errors.New("dispatch-worker /dispatch: unexpected status " + resp.Status)
	}
	return nil
}

func (s *server) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	// A failed east-west call must not crash the request; log it (with trace_id) and return a clean 502.
	telemetry.Logger.ErrorContext(r.Context(), "intake upstream failed", "what", what, "err", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream unavailable: " + what})
}

func main() {
	ctx := context.Background()
	shutdown, err := telemetry.Setup(ctx, "dispatch-intake")
	if err != nil {
		telemetry.Logger.Error("otel init failed; continuing without tracing", "err", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	shipmentsURL := getenv("SHIPMENTS_URL", "http://shipments")
	dispatchURL := getenv("DISPATCH_WORKER_URL", "http://dispatch-worker")
	srv := &server{
		shipments:   shipmentsclient.New(shipmentsURL),
		dispatchURL: dispatchURL,
		http:        telemetry.Client(),
	}
	handler := telemetry.WrapHandler(srv.routes(), "http.server")

	httpSrv := &http.Server{Addr: getenv("ADDR", ":8080"), Handler: handler, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}

	go func() {
		telemetry.Logger.Info("starting dispatch-intake", "addr", httpSrv.Addr, "shipments", shipmentsURL, "dispatch_worker", dispatchURL)
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
