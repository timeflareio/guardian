package config

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// The dashboard's credential lives in the config file as a bcrypt hash rather
// than as a password. The file is already 0600, so the file mode is not the
// argument: `config doctor` and `config list` print effective values, every key
// inherits a GUARDIAN_<KEY> environment override (readable through `docker
// inspect` and /proc/<pid>/environ), and a password reused from elsewhere is
// worth more to an attacker than dashboard access alone. A verifier only needs
// to check, never to present, so it needs no secret at rest at all.

// HashPassword hashes a dashboard password for storage in the config file.
//
// The cost factor is bcrypt's default, which is also the brute-force throttle:
// roughly 60 ms per verification makes online guessing useless against a
// generated password, and the same cost bounds what an unauthenticated caller
// can make the daemon spend per request.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hash), nil
}

// ValidatePasswordHash checks that a stored value is a bcrypt hash. A password
// pasted into the field by hand is the mistake this catches, and catching it at
// set time beats discovering it as a dashboard that rejects every credential.
func ValidatePasswordHash(hash string) error {
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return fmt.Errorf("not a bcrypt hash — set it with 'guardiand config set-dashboard-password': %w", err)
	}
	return nil
}

// DashboardBindsBeyondLoopback reports whether the dashboard listener is
// reachable from beyond this host.
//
// This is the bind address, not actual reachability: a container binds 0.0.0.0
// for -p to publish anything, and what was published is invisible from in here.
// Anything not demonstrably loopback counts as exposed, including an
// unresolvable hostname — the failure of a guess in the other direction is a
// page served without a credential.
func (cfg *Config) DashboardBindsBeyondLoopback() bool {
	host := strings.TrimSpace(cfg.BindAddress)
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback()
	}
	return true
}

// DashboardAuthRequired reports whether the dashboard must present a credential
// before it is served. Reads on loopback are exempt: there is no exposure to
// defend, and forcing a password on a developer's 127.0.0.1 is ceremony. The
// exemption is argued entirely from what a GET gives away, so it covers reads
// only — a surface that signs is reachable by any local process.
func (cfg *Config) DashboardAuthRequired() bool {
	return cfg.DashboardBindsBeyondLoopback()
}

// DashboardWithheld reports whether the dashboard is enabled but must not be
// served, because it would be exposed without a credential.
//
// The daemon continues in this state. Refusing to start would fail a guardian's
// economic function — a missed reveal window is slashable — over a dashboard
// misconfiguration, which would cost the operator real amounts to protect a
// page. The proportionate failure is a missing page, loudly explained.
func (cfg *Config) DashboardWithheld() bool {
	return cfg.EnableDashboard && cfg.DashboardAuthRequired() && cfg.DashboardPasswordHash == ""
}

// DashboardTLSEnabled reports whether the dashboard serves over TLS. Validate
// guarantees the pair is complete, so either path answers.
func (cfg *Config) DashboardTLSEnabled() bool {
	return cfg.DashboardTLSCertFile != "" && cfg.DashboardTLSKeyFile != ""
}

// DashboardIdentity names this guardian for the authentication realm, so a
// browser's saved-credential list stays legible when an operator runs several.
// Deliberately not monitor_name, which defaults to the same string for every
// guardian and would make every realm identical.
func (cfg *Config) DashboardIdentity() string {
	if cfg.GuardianID != "" {
		return cfg.GuardianID
	}
	return cfg.KeyName
}
