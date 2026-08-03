package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubSource is a Source with fixed answers, so handler behaviour is tested
// without a chain or a daemon.
type stubSource struct {
	vitals      Vitals
	assignments Assignments
	economics   Economics
	keys        Keys
	config      Config
	activity    Activity
	// slow makes every section block, to exercise the handler timeout.
	slow bool
}

func (s *stubSource) maybeBlock(ctx context.Context) {
	if !s.slow {
		return
	}
	<-ctx.Done()
}

func (s *stubSource) Vitals(ctx context.Context) Vitals { s.maybeBlock(ctx); return s.vitals }
func (s *stubSource) Assignments(ctx context.Context) Assignments {
	s.maybeBlock(ctx)
	return s.assignments
}
func (s *stubSource) Economics(ctx context.Context) Economics { s.maybeBlock(ctx); return s.economics }
func (s *stubSource) Keys(ctx context.Context) Keys           { s.maybeBlock(ctx); return s.keys }
func (s *stubSource) Config(ctx context.Context) Config       { s.maybeBlock(ctx); return s.config }
func (s *stubSource) Activity(ctx context.Context) Activity   { s.maybeBlock(ctx); return s.activity }

func get(t *testing.T, h http.Handler, path string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return res, string(body)
}

func TestEverySectionServesJSON(t *testing.T) {
	h := Handler(&stubSource{}, NoAuth())
	for _, path := range []string{
		"/api/vitals", "/api/assignments", "/api/economics",
		"/api/keys", "/api/config", "/api/activity",
	} {
		res, body := get(t, h, path)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: want 200, got %d", path, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s: want JSON content type, got %q", path, ct)
		}
		// A polling dashboard served a cached snapshot would show stale
		// economics, which is worse than showing none.
		if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: want no-store, got %q", path, cc)
		}
		var into map[string]any
		if err := json.Unmarshal([]byte(body), &into); err != nil {
			t.Errorf("%s: body is not valid JSON: %v", path, err)
		}
	}
}

func TestUnavailableSectionCarriesItsReason(t *testing.T) {
	// The load-bearing case: an unreachable node must not render as zeros. A
	// zeroed float panel and a dead endpoint look identical otherwise, and one
	// of them is an emergency.
	src := &stubSource{economics: Economics{
		Unavailable: Unavailable{Unavailable: true, Reason: "guardian record unavailable: connection refused"},
	}}
	res, body := get(t, Handler(src, NoAuth()), "/api/economics")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an unavailable section is still a successful response: got %d", res.StatusCode)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["unavailable"] != true {
		t.Error("unavailable flag missing from the payload")
	}
	if !strings.Contains(body, "connection refused") {
		t.Error("the reason must reach the operator verbatim, not be swallowed")
	}
}

func TestGoldenVitalsShape(t *testing.T) {
	// Golden shape rather than golden bytes: the field NAMES are the contract
	// the embedded UI reads, and pinning whole documents would fail on every
	// unrelated addition.
	src := &stubSource{vitals: Vitals{
		GuardianAddress: "tmflr1example",
		ChainID:         "timeflare-test",
		Version:         "v1.2.3",
		Uptime:          "1h0m0s",
		Running:         true,
		Registered:      true,
		Healthy:         true,
		ChainHeight:     1200,
		LastBlockHeight: 1198,
		HeightLag:       2,
		EventStream:     "websocket (polling fallback)",
		LastUpdate:      time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
	}}
	_, body := get(t, Handler(src, NoAuth()), "/api/vitals")

	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{
		"guardian_address", "chain_id", "version", "uptime", "running",
		"registered", "healthy", "chain_height", "last_block_height",
		"height_lag", "event_stream", "last_update",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("vitals payload is missing %q, which the UI reads", key)
		}
	}
	if got["height_lag"] != float64(2) {
		t.Errorf("height_lag should be stated, not derived by the client: got %v", got["height_lag"])
	}
}

func TestGoldenAssignmentShape(t *testing.T) {
	src := &stubSource{assignments: Assignments{
		CurrentHeight: 500,
		Active: []Assignment{{
			SecretID:               "1b4e28ba-2fa1-5d29-883f-0016d3cca427",
			ChainState:             "pending",
			LocalState:             "needs_reveal",
			MinShares:              3,
			MaxShares:              7,
			AcceptedCount:          5,
			BondUveil:              153600,
			RewardPoolUveil:        "7000000",
			RewardFloorUveil:       1000000,
			BlocksToWindowClose:    12,
			AtRisk:                 true,
			Urgency:                90,
			RiskNote:               "window open and closing in 12 blocks",
			BlocksToCommitDeadline: -5,
		}},
		AtRisk:               []Assignment{{SecretID: "1b4e28ba", Urgency: 90}},
		AwaitingConfirmation: []Assignment{},
		PendingReveal:        []Assignment{},
		StateCounts:          map[string]int{"needs_reveal": 1},
	}}
	_, body := get(t, Handler(src, NoAuth()), "/api/assignments")

	var got struct {
		CurrentHeight int64        `json:"current_height"`
		Active        []Assignment `json:"active"`
		AtRisk        []Assignment `json:"at_risk"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got.Active) != 1 {
		t.Fatalf("want one active assignment, got %d", len(got.Active))
	}
	a := got.Active[0]
	if a.RewardFloorUveil != 1000000 {
		t.Errorf("reward floor should survive the round trip, got %d", a.RewardFloorUveil)
	}
	// A passed deadline stays negative rather than clamping: "passed" and
	// "due now" are different operational states.
	if a.BlocksToCommitDeadline != -5 {
		t.Errorf("a passed deadline must stay negative, got %d", a.BlocksToCommitDeadline)
	}
	if !a.AtRisk || a.RiskNote == "" {
		t.Error("at-risk assignments must carry a note explaining why")
	}
	if len(got.AtRisk) != 1 {
		t.Error("the at-risk list must be served separately for its own panel")
	}
}

func TestEmptyCollectionsSerialiseAsArrays(t *testing.T) {
	// The UI calls .length on these. A nil slice encodes as null, which would
	// throw in the browser rather than render an empty panel.
	src := &stubSource{
		assignments: Assignments{Active: []Assignment{}, AwaitingConfirmation: []Assignment{},
			PendingReveal: []Assignment{}, AtRisk: []Assignment{}},
		activity: Activity{Decisions: []ActivityDecision{}, Submissions: []ActivitySubmission{},
			Settlements: []ActivitySettlement{}},
	}
	h := Handler(src, NoAuth())

	_, body := get(t, h, "/api/assignments")
	for _, key := range []string{`"active": []`, `"at_risk": []`, `"pending_reveal": []`} {
		if !strings.Contains(body, key) {
			t.Errorf("expected %s in the payload, got:\n%s", key, body)
		}
	}
	_, body = get(t, h, "/api/activity")
	for _, key := range []string{`"decisions": []`, `"submissions": []`} {
		if !strings.Contains(body, key) {
			t.Errorf("expected %s in the payload", key)
		}
	}
}

func TestActivityAlwaysStatesTheSinceStartLimit(t *testing.T) {
	// A restarted daemon's empty history must not read as "nothing happened".
	src := &stubSource{activity: Activity{
		Note:      "Since this process started — a restart clears these.",
		StartedAt: time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC),
	}}
	_, body := get(t, Handler(src, NoAuth()), "/api/activity")
	if !strings.Contains(body, "Since this process started") {
		t.Error("every activity payload must carry the since-start caveat")
	}
	if !strings.Contains(body, "started_at") {
		t.Error("started_at must be present so the caveat is checkable")
	}
}

func TestPageIsServedFromRoot(t *testing.T) {
	res, body := get(t, Handler(&stubSource{}, NoAuth()), "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the embedded page should serve from /, got %d", res.StatusCode)
	}
	if !strings.Contains(body, "timeflare") {
		t.Error("served page does not look like the dashboard")
	}
	// The brand tokens are copied into the page; a missing provenance comment
	// means someone edited assets without reading where they came from.
	if !strings.Contains(body, "timeflare-brand-1a.md") {
		t.Error("the page must name its brand-token provenance")
	}
}

func TestScriptIsServed(t *testing.T) {
	res, body := get(t, Handler(&stubSource{}, NoAuth()), "/app.js")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("app.js should be served, got %d", res.StatusCode)
	}
	if !strings.Contains(body, "pollOnce") {
		t.Error("app.js does not look like the dashboard script")
	}
}

func TestHandlerBoundsSlowSections(t *testing.T) {
	// A section that hangs on an unreachable node must not pin the request
	// forever, or a polling UI stacks requests against a dead endpoint.
	src := &stubSource{slow: true}
	h := Handler(src, NoAuth())

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
		ctx, cancel := context.WithTimeout(req.Context(), 200*time.Millisecond)
		defer cancel()
		h.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return once its context was cancelled")
	}
}

func TestOnlyGETIsRouted(t *testing.T) {
	// Read-only means read-only: nothing here mutates, so a POST should not
	// reach a handler at all.
	h := Handler(&stubSource{}, NoAuth())
	req := httptest.NewRequest(http.MethodPost, "/api/vitals", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Result().StatusCode == http.StatusOK {
		t.Error("POST must not be served by a read-only dashboard")
	}
}
