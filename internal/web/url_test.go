package web

import (
	"net/http/httptest"
	"testing"
)

func TestAbsForRequestUsesRequestHostWhenBaseURLIsLocal(t *testing.T) {
	r := httptest.NewRequest("GET", "http://127.0.0.1/admin/settings/automation", nil)
	r.Host = "cms.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")

	got := (&Server{baseURL: "http://localhost:8080"}).absForRequest(r, "/api/admin/v1")
	want := "https://cms.example.com/api/admin/v1"
	if got != want {
		t.Fatalf("absForRequest() = %q, want %q", got, want)
	}
}

func TestAbsForRequestKeepsConfiguredPublicBaseURL(t *testing.T) {
	r := httptest.NewRequest("GET", "http://127.0.0.1/admin/settings/automation", nil)
	r.Host = "proxy.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")

	got := (&Server{baseURL: "https://ccvar.com"}).absForRequest(r, "/api/admin/v1")
	want := "https://ccvar.com/api/admin/v1"
	if got != want {
		t.Fatalf("absForRequest() = %q, want %q", got, want)
	}
}
