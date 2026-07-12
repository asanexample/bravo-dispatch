package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asanexample/bravo-dispatch/internal/awsqueue"
	"github.com/asanexample/bravo-dispatch/internal/shipments"
	"github.com/asanexample/bravo-dispatch/internal/shipmentsclient"
	"github.com/open-feature/go-sdk/openfeature"
)

func TestHealthz(t *testing.T) {
	srv := &server{queue: awsqueue.NewMemory()}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestDispatchMissingShipmentID(t *testing.T) {
	srv := &server{queue: awsqueue.NewMemory()}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/dispatch", bytes.NewReader([]byte(`{}`))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /dispatch (no shipmentId) = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDispatchEnqueuesAndReturns202(t *testing.T) {
	q := awsqueue.NewMemory()
	srv := &server{queue: q}

	body, _ := json.Marshal(dispatchRequest{ShipmentID: "BD-10023"})
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/dispatch", bytes.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /dispatch = %d, want %d, body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msgs, err := q.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive after enqueue: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("Receive returned %d messages, want 1", len(msgs))
	}
	var got dispatchRequest
	if err := json.Unmarshal([]byte(msgs[0].Body), &got); err != nil {
		t.Fatalf("decode enqueued message: %v", err)
	}
	if got.ShipmentID != "BD-10023" {
		t.Errorf("enqueued shipmentId = %q, want BD-10023", got.ShipmentID)
	}
}

// fakeFlags is a flagEvaluator test double that always returns a fixed boolean, so worker.process tests don't
// need a real flagd resolver.
type fakeFlags struct{ value bool }

func (f fakeFlags) BooleanValue(context.Context, string, bool, openfeature.EvaluationContext, ...openfeature.Option) (bool, error) {
	return f.value, nil
}

// spyEvents is an awssns.Publisher test double that records every Publish call.
type spyEvents struct{ published []string }

func (s *spyEvents) Backend() string { return "spy" }
func (s *spyEvents) Publish(_ context.Context, body string) error {
	s.published = append(s.published, body)
	return nil
}

func newFakeShipmentsServer(t *testing.T, sh shipments.Shipment) (*httptest.Server, *shipments.StatusUpdate) {
	t.Helper()
	var lastUpdate shipments.StatusUpdate
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shipments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != sh.ID {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sh)
	})
	mux.HandleFunc("POST /shipments/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != sh.ID {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&lastUpdate)
		updated := sh
		updated.Status = lastUpdate.Status
		updated.CurrentLocation = lastUpdate.CurrentLocation
		updated.ETA = lastUpdate.ETA
		updated.Carrier = lastUpdate.Carrier
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(updated)
	})
	return httptest.NewServer(mux), &lastUpdate
}

func TestWorkerProcessPriorityAssignsFastLaneAndNotifies(t *testing.T) {
	sh := shipments.Shipment{ID: "BD-10023", Origin: "Springfield Sort Center", Status: shipments.Created}
	shipmentsSrv, lastUpdate := newFakeShipmentsServer(t, sh)
	defer shipmentsSrv.Close()

	var notifyBody map[string]any
	notifySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			t.Errorf("unexpected notify path %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&notifyBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer notifySrv.Close()

	events := &spyEvents{}
	w := &worker{
		shipments: shipmentsclient.New(shipmentsSrv.URL),
		events:    events,
		notifyURL: notifySrv.URL,
		http:      http.DefaultClient,
		flags:     fakeFlags{value: true},
		pick:      func(int) int { t.Fatal("pick should not be consulted on the priority path"); return 0 },
	}

	body, _ := json.Marshal(dispatchRequest{ShipmentID: sh.ID})
	if err := w.process(context.Background(), string(body)); err != nil {
		t.Fatalf("process: %v", err)
	}

	if lastUpdate.Status != shipments.PickedUp {
		t.Errorf("status update sent Status = %q, want %q", lastUpdate.Status, shipments.PickedUp)
	}
	if lastUpdate.Carrier != "Bravo Dispatch Priority Air" {
		t.Errorf("status update sent Carrier = %q, want the priority carrier", lastUpdate.Carrier)
	}
	if len(events.published) != 1 {
		t.Errorf("SNS Publish called %d times, want 1", len(events.published))
	}
	if notifyBody["shipmentId"] != sh.ID {
		t.Errorf("notify shipmentId = %v, want %v", notifyBody["shipmentId"], sh.ID)
	}
	if notifyBody["carrier"] != "Bravo Dispatch Priority Air" {
		t.Errorf("notify carrier = %v, want the priority carrier", notifyBody["carrier"])
	}
}

func TestWorkerProcessNormalUsesPickFunc(t *testing.T) {
	sh := shipments.Shipment{ID: "BD-10041", Origin: "Lakeside Sort Center", Status: shipments.Created}
	shipmentsSrv, lastUpdate := newFakeShipmentsServer(t, sh)
	defer shipmentsSrv.Close()
	notifySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer notifySrv.Close()

	w := &worker{
		shipments: shipmentsclient.New(shipmentsSrv.URL),
		events:    &spyEvents{},
		notifyURL: notifySrv.URL,
		http:      http.DefaultClient,
		flags:     fakeFlags{value: false},
		pick:      func(int) int { return 1 }, // "Bravo Dispatch Air", second normal carrier
	}

	body, _ := json.Marshal(dispatchRequest{ShipmentID: sh.ID})
	if err := w.process(context.Background(), string(body)); err != nil {
		t.Fatalf("process: %v", err)
	}
	if lastUpdate.Carrier != "Bravo Dispatch Air" {
		t.Errorf("Carrier = %q, want Bravo Dispatch Air", lastUpdate.Carrier)
	}
}

func TestWorkerProcessUnknownShipmentDropsMessage(t *testing.T) {
	sh := shipments.Shipment{ID: "BD-10023", Origin: "Springfield Sort Center"}
	shipmentsSrv, _ := newFakeShipmentsServer(t, sh)
	defer shipmentsSrv.Close()

	w := &worker{
		shipments: shipmentsclient.New(shipmentsSrv.URL),
		events:    &spyEvents{},
		notifyURL: "http://notify.invalid",
		http:      http.DefaultClient,
		flags:     fakeFlags{value: false},
		pick:      func(int) int { return 0 },
	}

	body, _ := json.Marshal(dispatchRequest{ShipmentID: "BD-NOPE"})
	if err := w.process(context.Background(), string(body)); err != nil {
		t.Fatalf("process(unknown shipment) = %v, want nil (drop, not redeliver)", err)
	}
}

func TestWorkerProcessShipmentsUnreachableReturnsError(t *testing.T) {
	w := &worker{
		shipments: shipmentsclient.New("http://127.0.0.1:0"),
		events:    &spyEvents{},
		notifyURL: "http://notify.invalid",
		http:      http.DefaultClient,
		flags:     fakeFlags{value: false},
		pick:      func(int) int { return 0 },
	}

	body, _ := json.Marshal(dispatchRequest{ShipmentID: "BD-10023"})
	if err := w.process(context.Background(), string(body)); err == nil {
		t.Fatal("process(shipments unreachable) = nil, want an error (so the message is redelivered)")
	}
}
