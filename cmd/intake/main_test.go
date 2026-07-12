package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asanexample/bravo-dispatch/internal/shipments"
	"github.com/asanexample/bravo-dispatch/internal/shipmentsclient"
)

func TestHealthz(t *testing.T) {
	srv := &server{shipments: shipmentsclient.New("http://shipments.invalid"), dispatchURL: "http://dispatch-worker.invalid", http: http.DefaultClient}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCreateShipmentValidationError(t *testing.T) {
	srv := &server{shipments: shipmentsclient.New("http://shipments.invalid"), dispatchURL: "http://dispatch-worker.invalid", http: http.DefaultClient}
	body, _ := json.Marshal(createShipmentRequest{Recipient: "", Origin: "O", Destination: "D"})
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/shipments", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /shipments (missing recipient) = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCreateShipmentUpstreamUnavailable(t *testing.T) {
	// No shipments service is running at this address, so intake must fail cleanly (502), not hang.
	srv := &server{shipments: shipmentsclient.New("http://127.0.0.1:0"), dispatchURL: "http://dispatch-worker.invalid", http: http.DefaultClient}
	body, _ := json.Marshal(createShipmentRequest{Recipient: "R", Origin: "O", Destination: "D"})
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/shipments", bytes.NewReader(body)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("POST /shipments (upstream down) = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

// TestCreateShipmentHappyPath exercises the full orchestration against fake shipments + dispatch-worker
// backends: create must call both and return the created shipment.
func TestCreateShipmentHappyPath(t *testing.T) {
	var dispatchCalled bool
	var dispatchBody map[string]string

	shipmentsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/shipments" {
			t.Errorf("unexpected shipments request: %s %s", r.Method, r.URL.Path)
		}
		var req shipments.CreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		sh := shipments.Shipment{ID: "BD-42000", Recipient: req.Recipient, Origin: req.Origin, Destination: req.Destination, Status: shipments.Created}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(sh)
	}))
	defer shipmentsSrv.Close()

	dispatchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/dispatch" {
			t.Errorf("unexpected dispatch-worker request: %s %s", r.Method, r.URL.Path)
		}
		dispatchCalled = true
		_ = json.NewDecoder(r.Body).Decode(&dispatchBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer dispatchSrv.Close()

	srv := &server{
		shipments:   shipmentsclient.New(shipmentsSrv.URL),
		dispatchURL: dispatchSrv.URL,
		http:        http.DefaultClient,
	}

	body, _ := json.Marshal(createShipmentRequest{Recipient: "Dana Okafor", Origin: "Test Depot", Destination: "Test Town"})
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/shipments", bytes.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /shipments = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var got shipments.Shipment
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != "BD-42000" {
		t.Errorf("returned shipment ID = %q, want BD-42000", got.ID)
	}
	if !dispatchCalled {
		t.Fatal("dispatch-worker /dispatch was never called")
	}
	if dispatchBody["shipmentId"] != "BD-42000" {
		t.Errorf("dispatch request shipmentId = %q, want BD-42000", dispatchBody["shipmentId"])
	}
}
