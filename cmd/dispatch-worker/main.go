// Command dispatch-worker is Bravo Dispatch's routing/dispatch service (team bravo, product dispatch, service
// dispatch-worker).
//
// It is BOTH the producer and the consumer of its own SQS queue (REQUESTS_QUEUE_URL): its HTTP handler
// (POST /dispatch) enqueues a dispatch request and returns immediately (202 Accepted), decoupling the caller
// (intake) from the actual processing; a background goroutine long-polls the same queue and, per message,
// evaluates the enable-priority-routing feature flag, picks a simulated carrier/route, advances the
// shipment's status (east-west call to shipments), publishes a proof-of-capability event to its own SNS topic
// (EVENTS_TOPIC_ARN), and delivers the real fan-out notification (east-west call to notify). Producer+consumer
// in one service is deliberate: this platform's Crossplane Composition scopes every self-service AWS resource
// to exactly one owning (service, resourceName) pair, so two services can never share a physical queue/topic —
// see the platform ADR-073 self-service contract and this repo's README.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asanexample/bravo-dispatch/internal/awsqueue"
	"github.com/asanexample/bravo-dispatch/internal/awssns"
	"github.com/asanexample/bravo-dispatch/internal/carrier"
	"github.com/asanexample/bravo-dispatch/internal/flags"
	"github.com/asanexample/bravo-dispatch/internal/securerand"
	"github.com/asanexample/bravo-dispatch/internal/shipments"
	"github.com/asanexample/bravo-dispatch/internal/shipmentsclient"
	"github.com/asanexample/bravo-dispatch/internal/telemetry"
	"github.com/open-feature/go-sdk/openfeature"
)

// dispatchRequest is both the HTTP handler's request body and the SQS message shape — the handler just
// re-marshals it onto the queue.
type dispatchRequest struct {
	ShipmentID string `json:"shipmentId"`
}

// server is the HTTP-facing half: it only ever enqueues.
type server struct {
	queue awsqueue.Queue
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	log := telemetry.Logger

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("POST /dispatch", func(w http.ResponseWriter, r *http.Request) {
		var req dispatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ShipmentID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "shipmentId is required"})
			return
		}
		body, err := json.Marshal(req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode failed"})
			return
		}
		if err := s.queue.Send(r.Context(), string(body)); err != nil {
			log.ErrorContext(r.Context(), "dispatch enqueue failed", "shipmentId", req.ShipmentID, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "enqueue failed"})
			return
		}
		log.InfoContext(r.Context(), "dispatch request enqueued", "shipmentId", req.ShipmentID)
		writeJSON(w, http.StatusAccepted, map[string]string{"shipmentId": req.ShipmentID, "status": "queued"})
	})

	return mux
}

// flagEvaluator is the sliver of *openfeature.Client the worker needs — abstracted so tests can fake a flag
// value without depending on OpenFeature's resolver machinery.
type flagEvaluator interface {
	BooleanValue(ctx context.Context, flag string, defaultValue bool, evalCtx openfeature.EvaluationContext, options ...openfeature.Option) (bool, error)
}

// worker is the background-consumer half: it owns the actual routing/notification logic, split out from main
// so it's directly unit-testable independent of the SQS transport and the HTTP handler.
type worker struct {
	queue     awsqueue.Queue
	shipments *shipmentsclient.Client
	events    awssns.Publisher
	notifyURL string
	http      *http.Client
	flags     flagEvaluator
	pick      func(n int) int // carrier.Assign's selector; securerand.Intn in production, fixed in tests
}

// run long-polls the requests queue until ctx is cancelled, processing one batch at a time. A message is only
// deleted (acked) after process succeeds; a processing failure leaves it for SQS to redeliver (or eventually
// DLQ, per the queue's own redrive policy) rather than silently dropping it.
func (w *worker) run(ctx context.Context) {
	log := telemetry.Logger
	for {
		msgs, err := w.queue.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // shutting down
			}
			log.Error("dispatch-worker receive failed; retrying", "err", err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}
		for _, m := range msgs {
			if err := w.process(ctx, m.Body); err != nil {
				log.Error("dispatch-worker process failed; leaving message for redelivery", "err", err)
				continue
			}
			if err := w.queue.Delete(ctx, m.ReceiptHandle); err != nil {
				log.Error("dispatch-worker delete (ack) failed", "err", err)
			}
		}
	}
}

// process handles one dispatch-request message end to end: (a) evaluate enable-priority-routing, (b) pick a
// carrier/route, (c) advance the shipment's status, (d) publish a proof-of-capability SNS event, (e) deliver
// the real fan-out notification. Returns an error only for failures worth redelivering (transient upstream
// failures); an unknown shipment id is logged and dropped (redelivery can't fix a shipment that will never
// exist).
func (w *worker) process(ctx context.Context, body string) error {
	log := telemetry.Logger

	var req dispatchRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return fmt.Errorf("decode dispatch message: %w", err)
	}

	sh, ok, err := w.shipments.Shipment(ctx, req.ShipmentID)
	if err != nil {
		return fmt.Errorf("look up shipment %s: %w", req.ShipmentID, err)
	}
	if !ok {
		log.ErrorContext(ctx, "dispatch-worker: unknown shipment; dropping message", "shipmentId", req.ShipmentID)
		return nil
	}

	priority, err := w.flags.BooleanValue(ctx, "enable-priority-routing", false, flags.EvalContext(sh.ID))
	if err != nil {
		// A flag evaluation error still yields a value (the default, false) per OpenFeature's contract —
		// log it but keep going with the safe default rather than failing the whole message.
		log.ErrorContext(ctx, "flag evaluation failed; using default", "flag", "enable-priority-routing", "err", err)
	}
	pick := w.pick
	if pick == nil {
		pick = securerand.Intn
	}
	assignment := carrier.Assign(priority, pick)

	updated, found, err := w.shipments.UpdateStatus(ctx, sh.ID, shipmentsclient.UpdateStatusRequest{
		Status:          shipments.PickedUp,
		Label:           fmt.Sprintf("Picked up — assigned to %s", assignment.Carrier),
		CurrentLocation: sh.Origin + " sort facility",
		ETA:             time.Now().UTC().Add(assignment.ETA),
		Carrier:         assignment.Carrier,
	})
	if err != nil {
		return fmt.Errorf("update shipment %s status: %w", sh.ID, err)
	}
	if !found {
		// Raced with a delete between the Shipment() lookup above and here — nothing left to notify about.
		log.ErrorContext(ctx, "dispatch-worker: shipment disappeared mid-processing; dropping message", "shipmentId", sh.ID)
		return nil
	}

	// (d) Publish a proof-of-capability event to our own SNS topic. This is deliberately NOT the fan-out
	// delivery mechanism — nothing currently subscribes to this topic. It exists to prove the platform's
	// self-service SNS capability works end-to-end for this product; a real subscriber (e.g. an analytics
	// sink) can be added later without touching this call site. Publish failures are logged, not fatal —
	// the notify call below is what actually has to succeed.
	eventBody, err := json.Marshal(map[string]any{
		"event":      "shipment_status_changed",
		"shipmentId": updated.ID,
		"status":     updated.Status,
		"carrier":    updated.Carrier,
	})
	if err != nil {
		log.ErrorContext(ctx, "encode SNS event failed", "err", err)
	} else if err := w.events.Publish(ctx, string(eventBody)); err != nil {
		log.ErrorContext(ctx, "SNS event publish failed (non-fatal; proves capability, no current subscriber)", "err", err)
	}

	// (e) The actual fan-out delivery mechanism: notify's downstream simulations.
	if err := w.notify(ctx, updated, assignment); err != nil {
		return fmt.Errorf("notify %s: %w", updated.ID, err)
	}
	return nil
}

func (w *worker) notify(ctx context.Context, sh shipments.Shipment, assignment carrier.Assignment) error {
	payload, err := json.Marshal(map[string]any{
		"shipmentId": sh.ID,
		"status":     sh.Status,
		"carrier":    sh.Carrier,
		"label":      fmt.Sprintf("Picked up — assigned to %s", assignment.Carrier),
		"eta":        sh.ETA,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.notifyURL+"/events", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notify /events: unexpected status %s", resp.Status)
	}
	return nil
}

func main() {
	ctx := context.Background()
	shutdown, err := telemetry.Setup(ctx, "dispatch-dispatch-worker")
	if err != nil {
		telemetry.Logger.Error("otel init failed; continuing without tracing", "err", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	shipmentsURL := getenv("SHIPMENTS_URL", "http://shipments")
	notifyURL := getenv("NOTIFY_URL", "http://notify")

	queue, err := awsqueue.Open(ctx, os.Getenv("REQUESTS_QUEUE_URL"))
	if err != nil {
		telemetry.Logger.Error("requests queue init failed", "err", err)
		os.Exit(1)
	}
	events, err := awssns.Open(ctx, os.Getenv("EVENTS_TOPIC_ARN"))
	if err != nil {
		telemetry.Logger.Error("events topic init failed", "err", err)
		os.Exit(1)
	}
	telemetry.Logger.Info("dispatch-worker backends", "queue", queue.Backend(), "events", events.Backend())

	// Feature flags via flagship's sync source (ADR-099). Unset FLAGSHIP_SYNC_URL → in-code default (false).
	flagClient, err := flags.Setup(ctx, getenv("FLAGSHIP_SYNC_URL", ""))
	if err != nil {
		telemetry.Logger.Error("feature-flag init failed; using in-code defaults", "err", err)
		flagClient = openfeature.NewClient("bravo-dispatch")
	}

	srv := &server{queue: queue}
	wrk := &worker{
		queue:     queue,
		shipments: shipmentsclient.New(shipmentsURL),
		events:    events,
		notifyURL: notifyURL,
		http:      telemetry.Client(),
		flags:     flagClient,
	}

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	go wrk.run(workerCtx)

	handler := telemetry.WrapHandler(srv.routes(), "http.server")
	httpSrv := &http.Server{Addr: getenv("ADDR", ":8080"), Handler: handler, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}

	go func() {
		telemetry.Logger.Info("starting dispatch-dispatch-worker", "addr", httpSrv.Addr, "shipments", shipmentsURL, "notify", notifyURL)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			telemetry.Logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	telemetry.Logger.Info("shutting down (draining in-flight requests)…")
	cancelWorker()
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
