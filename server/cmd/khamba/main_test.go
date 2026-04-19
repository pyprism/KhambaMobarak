package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"khamba/internal/config"

	"github.com/spf13/cobra"
)

func newConfigOverrideCommand(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().IntP("port", "p", 0, "")
	cmd.Flags().StringP("db", "d", "", "")
	return cmd
}

func TestApplyConfigOverridesLeavesDefaultsWhenFlagsUnset(t *testing.T) {
	cmd := newConfigOverrideCommand(t)
	cfg := &config.Config{Port: 8080, DBPath: "/tmp/default.db"}

	if err := applyConfigOverrides(cmd, cfg); err != nil {
		t.Fatalf("applyConfigOverrides returned error: %v", err)
	}

	if cfg.Port != 8080 {
		t.Fatalf("expected port to remain unchanged, got %d", cfg.Port)
	}
	if cfg.DBPath != "/tmp/default.db" {
		t.Fatalf("expected db path to remain unchanged, got %q", cfg.DBPath)
	}
}

func TestApplyConfigOverridesAppliesExplicitFlags(t *testing.T) {
	cmd := newConfigOverrideCommand(t)
	if err := cmd.Flags().Set("port", "9000"); err != nil {
		t.Fatalf("failed to set port flag: %v", err)
	}
	if err := cmd.Flags().Set("db", "/var/lib/khamba/custom.db"); err != nil {
		t.Fatalf("failed to set db flag: %v", err)
	}

	cfg := &config.Config{Port: 8080, DBPath: "/tmp/default.db"}
	if err := applyConfigOverrides(cmd, cfg); err != nil {
		t.Fatalf("applyConfigOverrides returned error: %v", err)
	}

	if cfg.Port != 9000 {
		t.Fatalf("expected port 9000, got %d", cfg.Port)
	}
	if cfg.DBPath != "/var/lib/khamba/custom.db" {
		t.Fatalf("expected overridden db path, got %q", cfg.DBPath)
	}
}

func TestPostDummyEventSendsBearerTokenAndPayload(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST method, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("expected bearer token header, got %q", got)
		}
		if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Fatalf("expected application/json content-type, got %q", got)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}
		if payload["event_type"] != "heartbeat" {
			t.Fatalf("expected event_type heartbeat, got %#v", payload["event_type"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer server.Close()

	if err := postDummyEvent(server.URL+"/api/events", "test-token", "heartbeat"); err != nil {
		t.Fatalf("postDummyEvent returned error: %v", err)
	}
}

func TestPostDummyEventReturnsErrorOnHTTPFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid device token"}`))
	}))
	defer server.Close()

	err := postDummyEvent(server.URL+"/api/events", "bad-token", "boot")
	if err == nil {
		t.Fatalf("expected error for unauthorized response")
	}
	if !strings.Contains(err.Error(), "unexpected status 401") {
		t.Fatalf("expected status detail in error, got %v", err)
	}
}
