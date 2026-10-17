package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The harness must require 403 from the real binary for unauthenticated admin requests
func TestCheckAdminRouteRefusesAnonymous(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"forbidden is the expected refusal", http.StatusForbidden, false},
		{"open route is caught", http.StatusOK, true},
		{"server error is not a refusal", http.StatusInternalServerError, true},
		{"unauthorized is not what the contract says", http.StatusUnauthorized, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/compliance/wallets" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			api := &apiClient{base: srv.URL, hc: &http.Client{Timeout: 5 * time.Second}}
			if err := checkAdminRouteRefusesAnonymous(api); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
