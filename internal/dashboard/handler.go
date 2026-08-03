package dashboard

import (
	"embed"
	"encoding/json"
	"net/http"
	"time"
)

// assets holds the embedded UI. go:embed cannot reach outside this module, so
// the brand tokens and logo are copied into assets/ with a provenance comment
// naming mobile-client/design/timeflare-brand-1a.md as the single place either
// is edited.
//
//go:embed assets
var assets embed.FS

// handlerTimeout bounds each snapshot assembly. A section that reads the chain
// can hang on an unreachable node; without this the polling UI would stack up
// requests against a dead endpoint. On timeout the section reports itself
// unavailable, which is the honest answer.
const handlerTimeout = 8 * time.Second

// Handler returns the dashboard's HTTP handler: the embedded page at / and one
// JSON snapshot endpoint per section, behind auth.
//
// Endpoints are versionless plain JSON by design (plan §2) — this serves one
// embedded UI shipped in the same binary, so there is no second consumer to
// keep compatible and a version negotiation would be ceremony.
//
// auth wraps the whole mux rather than each route, so a route added later
// cannot be reached unauthenticated: the shape must make an unauthenticated
// route impossible to add, not merely unlikely.
func Handler(src Source, auth Authenticator) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /", http.FileServerFS(pageFS()))
	mux.HandleFunc("GET /api/vitals", section(func(r *http.Request) any {
		return src.Vitals(r.Context())
	}))
	mux.HandleFunc("GET /api/assignments", section(func(r *http.Request) any {
		return src.Assignments(r.Context())
	}))
	mux.HandleFunc("GET /api/economics", section(func(r *http.Request) any {
		return src.Economics(r.Context())
	}))
	mux.HandleFunc("GET /api/keys", section(func(r *http.Request) any {
		return src.Keys(r.Context())
	}))
	mux.HandleFunc("GET /api/config", section(func(r *http.Request) any {
		return src.Config(r.Context())
	}))
	mux.HandleFunc("GET /api/activity", section(func(r *http.Request) any {
		return src.Activity(r.Context())
	}))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.authenticate(w, r) {
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// section wraps one snapshot assembler: bounded context, no caching, JSON out.
func section(assemble func(*http.Request) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := contextWithTimeout(r, handlerTimeout)
		defer cancel()

		body := assemble(r.WithContext(ctx))

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		// A polling dashboard must never be served a cached snapshot — a stale
		// float panel is worse than a slow one.
		w.Header().Set("Cache-Control", "no-store")
		// The page is same-origin and inline-only; these keep a stray browser
		// extension or embedded frame from doing anything with it.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		// Encoding into the ResponseWriter means a mid-encode failure cannot be
		// reported with a status code; nothing here can fail to marshal (plain
		// structs, no channels or funcs), so this is safe rather than lucky.
		_ = enc.Encode(body)
	}
}
