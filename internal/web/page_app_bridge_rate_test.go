package web

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestPageAppBridgeRemoteTrustsForwardedIPOnlyFromLoopback(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{
			name:       "public peer cannot spoof forwarded address",
			remoteAddr: "203.0.113.9:4567",
			forwarded:  "198.51.100.7",
			want:       "203.0.113.9",
		},
		{
			name:       "local reverse proxy forwards client address",
			remoteAddr: "127.0.0.1:4567",
			forwarded:  "198.51.100.7, 127.0.0.1",
			want:       "198.51.100.7",
		},
		{
			name:       "invalid forwarded address falls back to proxy",
			remoteAddr: "[::1]:4567",
			forwarded:  "not-an-ip",
			want:       "::1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/bridge", nil)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("X-Forwarded-For", test.forwarded)
			if got := pageAppBridgeRemote(request); got != test.want {
				t.Fatalf("remote = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPageAppBridgeRateRegistryPrunesExpiredAndEmptyBuckets(t *testing.T) {
	server := &Server{}
	pageAppBridgeRates.Delete(server)
	t.Cleanup(func() { pageAppBridgeRates.Delete(server) })
	registry := pageAppBridgeRateRegistryFor(server)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	registry.mu.Lock()
	registry.buckets["empty"] = nil
	registry.buckets["active"] = []time.Time{now.Add(-30 * time.Second)}
	for index := 0; index < 2048; index++ {
		registry.buckets["spoofed-"+strconv.Itoa(index)] = []time.Time{
			now.Add(-2 * time.Minute),
		}
	}
	registry.mu.Unlock()

	if !allowPageAppBridgeCall(server, "new-client", now) {
		t.Fatal("new client should be admitted after expired bucket cleanup")
	}
	registry.mu.Lock()
	if len(registry.buckets) != 2 ||
		len(registry.buckets["active"]) != 1 ||
		len(registry.buckets["new-client"]) != 1 {
		t.Fatalf("expired buckets were not reclaimed: %#v", registry.buckets)
	}
	registry.mu.Unlock()

	limit := pagePlatformServerLimits().MaxBridgeCallsPerMinute
	for index := 1; index < limit; index++ {
		if !allowPageAppBridgeCall(server, "new-client", now.Add(time.Duration(index)*time.Millisecond)) {
			t.Fatalf("client rejected before limit at request %d", index+1)
		}
	}
	if allowPageAppBridgeCall(server, "new-client", now.Add(59*time.Second)) {
		t.Fatal("client should be rejected after reaching the one-minute limit")
	}

	later := now.Add(2 * time.Minute)
	if !allowPageAppBridgeCall(server, "after-window", later) {
		t.Fatal("new request should be admitted after the prior window expires")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.buckets) != 1 || len(registry.buckets["after-window"]) != 1 {
		t.Fatalf("registry retained expired active buckets: %#v", registry.buckets)
	}
}
