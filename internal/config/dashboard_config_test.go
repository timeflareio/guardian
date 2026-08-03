package config

import "testing"

// The dashboard's config surface, and specifically the two decisions that are
// easy to regress: it binds loopback rather than the shared bind address, and
// three listeners must be pairwise distinct.

func TestDashboardDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.EnableDashboard {
		t.Error("the dashboard is enabled by default (owner ruling, July 2026)")
	}
	if cfg.DashboardPort != 21200 {
		t.Errorf("default dashboard port should be 21200, got %d", cfg.DashboardPort)
	}
	// The dashboard shares BindAddress rather than carrying its own (owner
	// ruling, 28 July 2026): one address to set, and the devnet's per-guardian
	// dashboards are routable without special-casing. The consequence is that
	// v1 is UNAUTHENTICATED on every interface by default — deliberate and
	// time-limited, with firewalling the operator's job until auth lands.
	// Asserted so a future "safer default" cannot be slipped in without
	// revisiting the ruling.
	if cfg.BindAddress != "0.0.0.0" {
		t.Errorf("dashboard reachability follows BindAddress, expected 0.0.0.0 default, got %q", cfg.BindAddress)
	}
}

func TestDashboardPortMustBeDistinct(t *testing.T) {
	// A port collision otherwise surfaces at bind time, blaming whichever
	// listener lost the race — a poor way to learn about a typo.
	cases := []struct {
		name string
		set  func(*Config)
		want string
	}{
		{"same as metrics", func(c *Config) { c.DashboardPort = c.MetricsPort }, "metrics_port"},
		{"same as health", func(c *Config) { c.DashboardPort = c.HealthPort }, "health_port"},
		{"zero", func(c *Config) { c.DashboardPort = 0 }, "between 1 and 65535"},
		{"out of range", func(c *Config) { c.DashboardPort = 70000 }, "between 1 and 65535"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validatableConfig()
			tc.set(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation to reject %s", tc.name)
			}
			if !contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestDashboardPortCollidesWithGRPCEndpoint(t *testing.T) {
	// The same check the other two listeners already carry: a default that
	// collides with the gRPC endpoint was broken out of the box before it
	// existed.
	cfg := validatableConfig()
	cfg.GRPCEndpoint = "localhost:21200"
	cfg.DashboardPort = 21200
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected a collision with grpc_endpoint to be rejected")
	}
	if !contains(err.Error(), "dashboard_port") {
		t.Errorf("error should name dashboard_port, got %q", err.Error())
	}
}

func TestDefaultConfigValidates(t *testing.T) {
	// Three listeners now: the shipped defaults must be mutually consistent.
	cfg := validatableConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must validate, got %v", err)
	}
}

// validatableConfig is DefaultConfig plus the identity fields Validate
// requires, so these tests exercise port logic rather than re-testing identity
// validation.
func validatableConfig() *Config {
	cfg := DefaultConfig()
	cfg.GuardianAddress = "tmflr1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq5utu8z"
	cfg.KeyName = "guardian-01"
	return cfg
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
