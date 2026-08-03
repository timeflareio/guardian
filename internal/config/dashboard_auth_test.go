package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dashboard's credential surface: what the config file accepts, what it
// shows, and — the load-bearing one — when the daemon must withhold the page.

func TestPasswordHashRoundTripsAndVerifies(t *testing.T) {
	hash, err := HashPassword("a chosen password")
	require.NoError(t, err)
	require.NoError(t, ValidatePasswordHash(hash))

	cfg := DefaultConfig()
	cfg.KeyName = "guardian"
	require.NoError(t, cfg.SetField("dashboard_password_hash", hash))
	assert.Equal(t, hash, cfg.DashboardPasswordHash)
	require.NoError(t, cfg.Validate())
}

func TestPlaintextPasswordRejectedAtSetTime(t *testing.T) {
	// The mistake this exists to catch: an operator setting the field to the
	// password itself, which would store a reusable secret in the config file
	// and authenticate nobody.
	cfg := DefaultConfig()
	err := cfg.SetField("dashboard_password_hash", "hunter2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set-dashboard-password",
		"the error must name the command that does it properly")

	// The environment override is the same path, so it is covered too.
	t.Setenv("GUARDIAN_DASHBOARD_PASSWORD_HASH", "hunter2")
	assert.Error(t, cfg.ApplyEnvOverrides())
}

func TestHashListedAsPresenceNotValue(t *testing.T) {
	// A 60-character blob in an operator's config report is noise. `config get`
	// still answers with the stored value for anyone who asks deliberately.
	hash, err := HashPassword("a chosen password")
	require.NoError(t, err)

	m := NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, m.Set("dashboard_password_hash", hash))

	item := m.ListAllGrouped()["Monitoring"]["dashboard_password_hash"]
	assert.Equal(t, "set", item.Value)
	assert.False(t, item.IsDefault, "a configured credential differs from the default")

	stored, err := m.Get("dashboard_password_hash")
	require.NoError(t, err)
	assert.Equal(t, hash, stored)

	unset := NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, unset.LoadOrDefault())
	assert.Equal(t, "not set", unset.ListAllGrouped()["Monitoring"]["dashboard_password_hash"].Value)
}

func TestTLSPathsAreAPair(t *testing.T) {
	cfg := DefaultConfig()
	cfg.KeyName = "guardian"

	cfg.DashboardTLSCertFile = "/etc/timeflare/dashboard.crt"
	require.Error(t, cfg.Validate(), "a certificate without a key must not reach listener setup")

	cfg.DashboardTLSKeyFile = "/etc/timeflare/dashboard.key"
	require.NoError(t, cfg.Validate())
	assert.True(t, cfg.DashboardTLSEnabled())

	cfg.DashboardTLSCertFile = ""
	require.Error(t, cfg.Validate(), "a key without a certificate must not either")
}

func TestBindAddressExposureClassification(t *testing.T) {
	for _, tc := range []struct {
		address string
		exposed bool
	}{
		{"0.0.0.0", true},
		{"", true},
		{"127.0.0.1", false},
		{"localhost", false},
		{"::1", false},
		{"10.0.0.5", true},
		{"guardian.example.com", true},
	} {
		cfg := DefaultConfig()
		cfg.BindAddress = tc.address
		assert.Equal(t, tc.exposed, cfg.DashboardBindsBeyondLoopback(),
			"bind address %q", tc.address)
	}
}

func TestDashboardWithheldOnlyWhenExposedWithoutCredential(t *testing.T) {
	hash, err := HashPassword("a chosen password")
	require.NoError(t, err)

	for _, tc := range []struct {
		name     string
		address  string
		hash     string
		enabled  bool
		withheld bool
	}{
		{"exposed without a credential", "0.0.0.0", "", true, true},
		{"exposed with a credential", "0.0.0.0", hash, true, false},
		{"loopback without a credential", "127.0.0.1", "", true, false},
		{"loopback with a credential", "127.0.0.1", hash, true, false},
		{"disabled entirely", "0.0.0.0", "", false, false},
	} {
		cfg := DefaultConfig()
		cfg.BindAddress = tc.address
		cfg.DashboardPasswordHash = tc.hash
		cfg.EnableDashboard = tc.enabled
		assert.Equal(t, tc.withheld, cfg.DashboardWithheld(), tc.name)
	}
}

func TestDefaultConfigWithholdsAnExposedDashboard(t *testing.T) {
	// The default is enable_dashboard=true on 0.0.0.0 with no credential, so
	// this is what a fresh install does: no page, and a guardian that reveals.
	cfg := DefaultConfig()
	assert.True(t, cfg.DashboardWithheld(),
		"a fresh install must not serve an unauthenticated dashboard beyond loopback")
}

func TestRealmIdentityFallsBackToKeyName(t *testing.T) {
	// Deliberately not monitor_name, which defaults to the same string for
	// every guardian and would make every realm identical.
	cfg := DefaultConfig()
	cfg.KeyName = "guardian-07"
	assert.Equal(t, "guardian-07", cfg.DashboardIdentity())

	cfg.GuardianID = "eu-west-primary"
	assert.Equal(t, "eu-west-primary", cfg.DashboardIdentity())
}
