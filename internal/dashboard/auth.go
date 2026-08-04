package dashboard

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// Username is the dashboard's single account name, fixed and not configurable.
//
// A username buys legibility here — `curl -u guardian:…`, one prompt shape in
// every browser — not defence: the value is in the docs and in this file, so
// treating it as a second secret would hide it from the operator and from
// nobody else. Where two secrets are wanted the honest form is a longer
// password. One account also means there is no authorisation model to express:
// no roles, no per-action rules, nothing to audit beyond access itself.
const Username = "guardian"

// failureLogWindow bounds how often one source's rejected attempts are logged.
// An unauthenticated endpoint that writes a line per request is a log-volume
// amplifier, which would trade one exposure for another.
const failureLogWindow = time.Minute

// failureSourceLimit caps the remembered sources, so an attacker cycling
// addresses cannot grow the map without bound. Past it the table is emptied and
// refills: the point is to bound log volume, not to keep an audit trail.
const failureSourceLimit = 1024

// Authenticator decides whether a dashboard request may proceed.
//
// Handler takes one, so a caller cannot forget to pass it, and the choice not
// to authenticate has to be written down as NoAuth rather than reached by
// omission — the same compile-time discipline Source already applies to the
// data direction.
type Authenticator interface {
	// authenticate reports whether the request may proceed. When it may not,
	// the implementation has already written the response.
	authenticate(w http.ResponseWriter, r *http.Request) bool
}

// NoAuth serves the dashboard without a credential. Correct only where there is
// no exposure to defend: a loopback bind address, where forcing a password on a
// developer's 127.0.0.1 is ceremony.
func NoAuth() Authenticator { return noAuth{} }

type noAuth struct{}

func (noAuth) authenticate(http.ResponseWriter, *http.Request) bool { return true }

// BasicAuth verifies HTTP Basic credentials against a bcrypt hash.
//
// Basic rather than a session cookie because browsers prompt for it natively
// and `curl -u` works: a login form would add a route, cookie signing or a
// session store, expiry, logout and a CSRF surface, all of which is state this
// adversary does not justify against a page where every route is a GET.
//
// The honest limit, which the docs repeat: without TLS this defends against
// unauthorised readers, not against a network eavesdropper. Base64 is not
// encryption, and the credential crosses the network on every poll.
func BasicAuth(realm, passwordHash string, logger *zap.Logger) Authenticator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &basicAuth{
		realm:      realm,
		hash:       []byte(passwordHash),
		logger:     logger,
		lastLogged: make(map[string]time.Time, 8),
	}
}

type basicAuth struct {
	realm  string
	hash   []byte
	logger *zap.Logger

	mu         sync.Mutex
	lastLogged map[string]time.Time
}

func (a *basicAuth) authenticate(w http.ResponseWriter, r *http.Request) bool {
	user, password, ok := r.BasicAuth()
	if !ok {
		a.reject(w, r, "no credentials")
		return false
	}

	// Both checks always run. Returning early on a username mismatch would skip
	// the bcrypt comparison and answer measurably faster, which is a timing
	// oracle for the username; subtle.ConstantTimeCompare covers the username
	// and bcrypt's own comparison covers the password.
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(Username)) == 1
	passwordOK := bcrypt.CompareHashAndPassword(a.hash, []byte(password)) == nil
	if !userOK || !passwordOK {
		// Deliberately one message for both halves: which one was wrong is
		// information the caller has not earned.
		a.reject(w, r, "invalid credentials")
		return false
	}
	return true
}

// reject answers 401 with the challenge a browser needs to prompt, and says
// nothing about which half of the credential failed.
func (a *basicAuth) reject(w http.ResponseWriter, r *http.Request, reason string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf("Basic realm=%q, charset=%q", a.realm, "UTF-8"))
	// A rejected response must not be cached and must not be sniffed, the same
	// as the snapshots themselves.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Error(w, "401 unauthorised", http.StatusUnauthorized)

	source := requestSource(r)
	if !a.shouldLog(source) {
		return
	}
	a.logger.Warn("Dashboard authentication rejected",
		zap.String("source", source),
		zap.String("reason", reason),
		zap.String("path", r.URL.Path),
		zap.Duration("suppressed_for", failureLogWindow))
}

// shouldLog rate-limits per source over a fixed window, so one noisy address
// cannot suppress the record of another.
func (a *basicAuth) shouldLog(source string) bool {
	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	if last, seen := a.lastLogged[source]; seen && now.Sub(last) < failureLogWindow {
		return false
	}
	if len(a.lastLogged) >= failureSourceLimit {
		a.lastLogged = make(map[string]time.Time, 8)
	}
	a.lastLogged[source] = now
	return true
}

// requestSource is the remote address without its port, so repeat attempts from
// one host collapse to one key rather than one per connection.
func requestSource(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
