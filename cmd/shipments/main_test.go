package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asanexample/bravo-dispatch/internal/awskv"
	"github.com/asanexample/bravo-dispatch/internal/shipments"
)

func newTestServer() *server {
	return &server{store: shipments.New(awskv.NewMemory())}
}

func TestHealthz(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCreateShipmentThenGet(t *testing.T) {
	srv := newTestServer()

	body, _ := json.Marshal(shipments.CreateRequest{Recipient: "Dana Okafor", Origin: "Test Depot", Destination: "Test Town"})
	req := httptest.NewRequest(http.MethodPost, "/shipments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /shipments = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var created shipments.Shipment
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("created shipment has no ID: %+v", created)
	}
	if created.Status != shipments.Created {
		t.Errorf("created status = %q, want %q", created.Status, shipments.Created)
	}

	// It's actually persisted: GET /shipments/{id} finds it.
	getReq := httptest.NewRequest(http.MethodGet, "/shipments/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /shipments/%s = %d, want %d", created.ID, getRec.Code, http.StatusOK)
	}
}

func TestCreateShipmentInvalidBody(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/shipments", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /shipments (bad body) = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestUpdateStatusThenGetReflectsChange(t *testing.T) {
	srv := newTestServer()

	createBody, _ := json.Marshal(shipments.CreateRequest{Recipient: "R", Origin: "Origin City", Destination: "D"})
	createRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(createRec, httptest.NewRequest(http.MethodPost, "/shipments", bytes.NewReader(createBody)))
	var created shipments.Shipment
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	upd := shipments.StatusUpdate{Status: shipments.PickedUp, Label: "Picked up", CurrentLocation: "Origin City sort facility", Carrier: "Bravo Dispatch Ground"}
	updBody, _ := json.Marshal(upd)
	updRec := httptest.NewRecorder()
	srv.routes().ServeHTTP(updRec, httptest.NewRequest(http.MethodPost, "/shipments/"+created.ID+"/status", bytes.NewReader(updBody)))
	if updRec.Code != http.StatusOK {
		t.Fatalf("POST /shipments/%s/status = %d, want %d, body=%s", created.ID, updRec.Code, http.StatusOK, updRec.Body.String())
	}
	var updated shipments.Shipment
	if err := json.Unmarshal(updRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode status update response: %v", err)
	}
	if updated.Status != shipments.PickedUp {
		t.Errorf("Status = %q, want %q", updated.Status, shipments.PickedUp)
	}
	if len(updated.Timeline) != 2 {
		t.Errorf("Timeline len = %d, want 2 (Created + PickedUp)", len(updated.Timeline))
	}
}

func TestUpdateStatusUnknownIDNotFound(t *testing.T) {
	srv := newTestServer()
	body, _ := json.Marshal(shipments.StatusUpdate{Status: shipments.PickedUp})
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/shipments/BD-99999/status", bytes.NewReader(body)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /shipments/BD-99999/status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
