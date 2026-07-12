package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asanexample/bravo-dispatch/internal/shipmentsclient"
)

func TestHealthz(t *testing.T) {
	srv := &server{shipments: shipmentsclient.New("http://shipments.invalid")}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestShipmentDetailUpstreamUnavailable(t *testing.T) {
	// No shipments service is running at this address, so the BFF must fail cleanly (502), not crash or hang.
	srv := &server{shipments: shipmentsclient.New("http://127.0.0.1:0")}

	req := httptest.NewRequest(http.MethodGet, "/api/shipments/BD-10023", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("GET /api/shipments/BD-10023 (upstream down) = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}
