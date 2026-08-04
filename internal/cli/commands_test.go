package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/timeflareio/guardian/internal/config"
)

// The listing renders whatever the registry declares, so a group the registry
// knows about has to reach the output. An unrecognised group is skipped rather
// than reported, so a listing that silently loses every value still looks like
// a successful run.
func TestConfigListShowsEveryRegistryGroup(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	out := g.mustRun("", "config", "list")
	for _, group := range config.GroupOrder() {
		if !strings.Contains(out, group) {
			t.Errorf("config list omitted the %q group:\n%s", group, out)
		}
	}
	for _, key := range []string{"chain_id", "key_name", "keyring_backend"} {
		if !strings.Contains(out, key) {
			t.Errorf("config list omitted %q:\n%s", key, out)
		}
	}
}

// health is what a process supervisor and a container probe call, so its exit
// status is the whole contract: zero only when the guardian says it is healthy.
func TestHealthAgainstAServer(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{name: "healthy", status: http.StatusOK, body: `{"status":"healthy","timestamp":"2026-08-03T00:00:00Z"}`},
		{name: "unhealthy status code", status: http.StatusServiceUnavailable, body: `{"status":"unhealthy"}`, wantErr: true},
		{name: "unparseable body", status: http.StatusOK, body: `not json`, wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer server.Close()

			g := newFixture(t)
			out, err := g.run("", "health", "--url", server.URL, "--timeout", "5")
			if c.wantErr && err == nil {
				t.Fatalf("expected failure, got success:\n%s", out)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected success, got %v\n%s", err, out)
			}
		})
	}
}

// --url has to be sufficient on its own: checking a guardian from a machine that
// is not running one is the point of the flag.
func TestHealthWithURLNeedsNoConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"healthy","timestamp":"2026-08-03T00:00:00Z"}`))
	}))
	defer server.Close()

	g := newFixture(t)
	if _, err := g.run("", "health", "--url", server.URL); err != nil {
		t.Fatalf("health --url failed with no configuration present: %v", err)
	}
}

// wallet create must produce a key the rest of the tooling can resolve, and show
// a mnemonic that imports back to the same address.
func TestWalletCreateThenShowAddressAgree(t *testing.T) {
	g := newFixture(t)
	created := g.mustRun("", "wallet", "create", "--name", "signer")

	address := strings.TrimSpace(g.mustRun("", "wallet", "show-address", "--name", "signer"))
	if address == "" {
		t.Fatal("show-address printed nothing")
	}
	if !strings.Contains(created, address) {
		t.Errorf("create reported a different address than show-address (%s):\n%s", address, created)
	}
	if !strings.HasPrefix(address, "tmflr1") {
		t.Errorf("address %q does not carry the chain's prefix", address)
	}

	// The mnemonic has to restore the same account, which means importing into a
	// different keyring: this one already holds the address, and the cosmos
	// keyring rightly refuses a second name for it.
	mnemonic := extractMnemonic(t, created)
	elsewhere := newFixture(t)
	imported := elsewhere.mustRun(mnemonic+"\n", "wallet", "import-from-mnemonic", "--name", "restored")
	if !strings.Contains(imported, address) {
		t.Errorf("the printed mnemonic restores a different account than %s:\n%s", address, imported)
	}
}

// The signing key is never silently replaced.
func TestWalletCreateRefusesExistingName(t *testing.T) {
	g := newFixture(t)
	g.mustRun("", "wallet", "create", "--name", "signer")
	before := strings.TrimSpace(g.mustRun("", "wallet", "show-address", "--name", "signer"))

	if _, err := g.run("", "wallet", "create", "--name", "signer"); err == nil {
		t.Fatal("wallet create overwrote an existing key")
	}
	if after := strings.TrimSpace(g.mustRun("", "wallet", "show-address", "--name", "signer")); after != before {
		t.Errorf("the signing key changed: %s -> %s", before, after)
	}
}

// status is a human report, not a probe: it succeeds at reporting even when what
// it reports is bad, and `health` is the command that turns an unhealthy
// guardian into a non-zero exit. What status must not do is imply health it did
// not observe.
func TestStatusReportsAnUnreachableChainAsUnhealthy(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	out := g.mustRun("", "status", "--timeout", "1")
	if !strings.Contains(out, "Unhealthy") {
		t.Errorf("status did not report the unreachable chain as unhealthy:\n%s", out)
	}
	if strings.Contains(out, "✅ Healthy") {
		t.Errorf("status claimed health with no chain reachable:\n%s", out)
	}
}

// The JSON form is what a script consumes, so it has to be parseable even on the
// degraded path where the chain answered nothing.
func TestStatusJSONIsParseableWhenDegraded(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	out := g.mustRun("", "status", "--format", "json", "--timeout", "1")
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in output:\n%s", out)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &report); err != nil {
		t.Fatalf("status --format json emitted unparseable JSON: %v\n%s", err, out)
	}
	if _, ok := report["registered"]; !ok {
		t.Errorf("status JSON is missing the registered field: %v", report)
	}
}

// register refuses before it can sign anything if the configured share key is
// not a valid one — the registration binds that key permanently for its epoch.
func TestRegisterRequiresAnEncryptionKey(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")
	g.mustRun("", "config", "set", "encryption-public-key", "")

	_, err := g.run("", "register", "--accept")
	if err == nil {
		t.Fatal("register proceeded with no encryption public key configured")
	}
	if !strings.Contains(err.Error(), "encryption public key") {
		t.Errorf("error does not name the missing key: %v", err)
	}
}

// update must refuse a no-op rather than signing and broadcasting nothing.
func TestUpdateRequiresAParameter(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	_, err := g.run("", "update")
	if err == nil {
		t.Fatal("update accepted an invocation with nothing to change")
	}
	if !strings.Contains(err.Error(), "no update parameters") {
		t.Errorf("error does not explain the refusal: %v", err)
	}
}
