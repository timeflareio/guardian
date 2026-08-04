package cli

import (
	"os"
	"strings"
	"testing"
)

// doctor is what an operator runs before trusting a host, so every branch that
// can say "this guardian is fine" has to be wrong only when it is.
func TestConfigDoctorPassesOnAHealthyGuardian(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	out := g.mustRun("", "config", "doctor")
	for _, want := range []string{"Validation:", "Signing key:", "Encryption key:", "operational"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output is missing %q:\n%s", want, out)
		}
	}
}

// The report has to mark what an operator changed, because the values they did
// not set are the ones they will not think to check.
func TestConfigDoctorMarksNonDefaults(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")
	g.mustRun("", "config", "set", "chain-id", "timeflare-mainnet")

	out := g.mustRun("", "config", "doctor")
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "chain_id") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("doctor did not report chain_id:\n%s", out)
	}
	if !strings.Contains(line, "*") {
		t.Errorf("chain_id differs from the default but is not marked: %q", line)
	}
	if !strings.Contains(line, "timeflare-mainnet") {
		t.Errorf("chain_id line does not show the effective value: %q", line)
	}
}

// A doctor that exits zero on a broken guardian is worse than no doctor.
func TestConfigDoctorFailsOnUnloadableKey(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	if err := os.Remove(g.get("encryption-private-key-path")); err != nil {
		t.Fatal(err)
	}

	out, err := g.run("", "config", "doctor")
	if err == nil {
		t.Fatalf("doctor reported success with no share key present:\n%s", out)
	}
	if !strings.Contains(out, "Encryption key:") {
		t.Errorf("doctor did not report on the encryption key:\n%s", out)
	}
}

// The address in the configuration and the address the key derives must agree;
// a guardian registered under one and signing with the other reveals nothing.
func TestConfigDoctorFailsOnAddressMismatch(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	// A syntactically valid address for this chain that is not the key's.
	g.mustRun("", "config", "set", "guardian-address",
		"tmflr1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp4c8ac")

	out, err := g.run("", "config", "doctor")
	if err == nil {
		t.Fatalf("doctor accepted an address that the signing key does not derive:\n%s", out)
	}
	if !strings.Contains(out, "does not match") {
		t.Errorf("doctor did not name the mismatch:\n%s", out)
	}
}

// doctor reports the effective view, so an environment override has to show up
// in it — that is the whole reason the command exists rather than `config list`.
func TestConfigDoctorReportsEnvironmentOverrides(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")
	t.Setenv("GUARDIAN_CHAIN_ID", "timeflare-from-env")

	out := g.mustRun("", "config", "doctor")
	if !strings.Contains(out, "timeflare-from-env") {
		t.Errorf("doctor did not report the environment override:\n%s", out)
	}
}
