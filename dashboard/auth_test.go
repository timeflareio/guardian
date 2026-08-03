package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"golang.org/x/crypto/bcrypt"
)

// testPassword and its hash, generated at the lowest cost bcrypt allows so the
// suite is not spending 60 ms per verification to prove routing.
const testPassword = "correct-horse-battery-staple"

func testHash(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hashing the test password: %v", err)
	}
	return string(hash)
}

// everyRoute is the mux's surface plus paths that do not exist. Authentication
// wraps the whole mux rather than each route, so coverage does not depend on
// this list staying complete — a route added later is behind the credential by
// construction, and the unknown paths here assert exactly that.
var everyRoute = []string{
	"/",
	"/app.js",
	"/api/vitals",
	"/api/assignments",
	"/api/economics",
	"/api/keys",
	"/api/config",
	"/api/activity",
	"/api/not-a-route-yet",
	"/../etc/passwd",
}

func TestEveryRouteRefusesWithoutCredentials(t *testing.T) {
	h := Handler(&stubSource{}, BasicAuth("test realm", testHash(t), zap.NewNop()))

	for _, path := range everyRoute {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: want 401 without credentials, got %d", path, rec.Code)
		}
		// Without the challenge a browser cannot prompt, and the page is simply
		// unreachable rather than protected.
		if challenge := rec.Header().Get("WWW-Authenticate"); !strings.Contains(challenge, "Basic") {
			t.Errorf("%s: want a Basic challenge, got %q", path, challenge)
		}
		if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "test realm") {
			t.Errorf("%s: the realm must name the guardian, got %q", path, rec.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestEveryRouteServesWithCredentials(t *testing.T) {
	h := Handler(&stubSource{}, BasicAuth("test realm", testHash(t), zap.NewNop()))

	for _, path := range []string{
		"/", "/api/vitals", "/api/assignments", "/api/economics",
		"/api/keys", "/api/config", "/api/activity",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth(Username, testPassword)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: want 200 with valid credentials, got %d", path, rec.Code)
		}
	}
}

func TestWrongCredentialsAreRefusedIdentically(t *testing.T) {
	h := Handler(&stubSource{}, BasicAuth("test realm", testHash(t), zap.NewNop()))

	cases := []struct {
		name     string
		user     string
		password string
	}{
		{"wrong password", Username, "not-the-password"},
		{"wrong username", "admin", testPassword},
		{"both wrong", "admin", "not-the-password"},
		{"empty password", Username, ""},
	}

	var bodies []string
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
		req.SetBasicAuth(tc.user, tc.password)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: want 401, got %d", tc.name, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}

	// Which half was wrong is information the caller has not earned: a response
	// that distinguishes them confirms the username to anyone probing.
	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Errorf("rejection bodies differ between cases (%q vs %q); the response must not say which half failed",
				bodies[0], bodies[i])
		}
	}
}

func TestNoAuthServesWithoutCredentials(t *testing.T) {
	// The loopback case: there is no exposure to defend, and a password on a
	// developer's 127.0.0.1 is ceremony.
	h := Handler(&stubSource{}, NoAuth())

	req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("NoAuth must serve without credentials, got %d", rec.Code)
	}
}

func TestRejectionsAreNotCached(t *testing.T) {
	h := Handler(&stubSource{}, BasicAuth("test realm", testHash(t), zap.NewNop()))

	req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("a 401 must not be cached, got Cache-Control %q", cc)
	}
}

func TestFailureLoggingIsRateLimitedPerSource(t *testing.T) {
	// An unauthenticated endpoint that logs a line per request is a log-volume
	// amplifier: one rejected source must produce one line, not a flood, while
	// a second source is still recorded.
	core, logs := observer.New(zap.WarnLevel)
	h := Handler(&stubSource{}, BasicAuth("test realm", testHash(t), zap.New(core)))

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
		req.RemoteAddr = "203.0.113.7:5000"
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if got := logs.Len(); got != 1 {
		t.Errorf("want one log line for 20 attempts from one source, got %d", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
	req.RemoteAddr = "198.51.100.9:5000"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := logs.Len(); got != 2 {
		t.Errorf("a second source must still be recorded: want 2 lines, got %d", got)
	}

	// The password must never be in the log — the point of the failure line is
	// the source, not the guess.
	for _, entry := range logs.All() {
		for _, field := range entry.Context {
			if strings.Contains(field.String, testPassword) {
				t.Error("a rejected credential must not be logged")
			}
		}
	}
}
