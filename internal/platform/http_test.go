package platform

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthBoundary(t *testing.T) {
	token := strings.Repeat("a", 40)
	app := &App{Config: Config{Brand: "test", Principals: []Principal{{Tenant: "a", Actor: "readonly", Role: "viewer", Token: token}}}}
	h := app.Handler()
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{{"GET", "/api/v1/overview", "", 401}, {"GET", "/api/v1/session", token, 200}, {"POST", "/api/v1/workloads", token, 403}, {"GET", "/api/v1/branding", "", 200}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("X-Tenant-ID", "attacker")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
		if tc.path == "/api/v1/session" && !strings.Contains(w.Body.String(), `"tenant":"a"`) {
			t.Fatal("tenant header trusted")
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing CSP")
		}
	}
}
func TestRateLimit(t *testing.T) {
	l := &limiter{windows: map[string]window{}}
	for i := 0; i < 180; i++ {
		if !l.allow("a") {
			t.Fatal("early limit")
		}
	}
	if l.allow("a") {
		t.Fatal("missing limit")
	}
	if !l.allow("b") {
		t.Fatal("cross-tenant rate limit")
	}
}
