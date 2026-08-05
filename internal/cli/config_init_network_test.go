package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/config"
)

// Selection exists to stop three related values being typed separately, so what
// matters is that all of them land together and that the transport comes with
// them. A guardian configured for the right endpoints under the wrong chain id
// looks healthy and fails every transaction.

// setupArgs are the flags that carry a `config init` through unattended, leaving
// the network for the test to decide.
func setupArgs(keyName string, dir string) []string {
	return []string{
		"config", "init",
		"--key-name", keyName,
		"--keyring-backend", "test",
		"--keyring-dir", dir,
		"--keyring-passphrase", "keyring-pass",
		"--auto-generate-key",
		"--encryption-key-passphrase", "at-rest-pass",
	}
}

func TestConfigInitWritesTheSelectedNetwork(t *testing.T) {
	for _, c := range []struct {
		name    string
		network []string
		wantID  string
		chainID string
		rpc     string
		grpc    string
		grpcTLS string
	}{
		{
			// A remote network arrives with TLS on, without the operator having to
			// know the key exists.
			name:    "remote network turns TLS on",
			network: []string{"--network", "testnet"},
			wantID:  "testnet",
			chainID: "timeflare-testnet-1",
			rpc:     "https://rpc.testnet.example.org",
			grpc:    "grpc.testnet.example.org:443",
			grpcTLS: "true",
		},
		{
			// Loopback is the one place the registry permits cleartext, and the
			// colocated node every devnet guardian runs is exactly that.
			name:    "loopback network leaves TLS off",
			network: []string{"--network", "devnet"},
			wantID:  "devnet",
			chainID: "timeflare-test",
			rpc:     "http://localhost:26657",
			grpc:    "localhost:9090",
			grpcTLS: "false",
		},
		{
			// Naming nothing takes the list's own default rather than whatever
			// literals this binary was compiled with.
			name:    "no network named takes the published default",
			network: nil,
			wantID:  "devnet",
			chainID: "timeflare-test",
			rpc:     "http://localhost:26657",
			grpc:    "localhost:9090",
			grpcTLS: "false",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newFixture(t)
			g.mustRun("", "wallet", "create", "--name", "guardian-one")
			g.mustRun("", append(setupArgs("guardian-one", g.dir), c.network...)...)

			for _, field := range []struct{ key, want string }{
				{"network", c.wantID},
				{"chain-id", c.chainID},
				{"rpc-endpoint", c.rpc},
				{"grpc-endpoint", c.grpc},
				{"grpc-tls", c.grpcTLS},
			} {
				if got := g.get(field.key); got != field.want {
					t.Errorf("%s = %q, want %q", field.key, got, field.want)
				}
			}
		})
	}
}

// A script naming a network that no longer exists has to fail rather than land
// somewhere else, and the failure has to say what was on offer.
func TestConfigInitRejectsAnUnknownNetwork(t *testing.T) {
	g := newFixture(t)
	g.mustRun("", "wallet", "create", "--name", "guardian-one")

	out, err := g.run("", append(setupArgs("guardian-one", g.dir), "--network", "mainnet")...)
	if err == nil {
		t.Fatalf("config init accepted a network that is not published:\n%s", out)
	}
	for _, want := range []string{"mainnet", "devnet", "testnet", config.CustomNetworkID} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// A network publishing no gRPC endpoint cannot serve this daemon, and that is
// worth learning at setup rather than at the first query.
func TestConfigInitRejectsANetworkWithoutGRPC(t *testing.T) {
	g := newFixture(t)
	g.mustRun("", "wallet", "create", "--name", "guardian-one")

	_, err := g.run("", append(setupArgs("guardian-one", g.dir), "--network", "restonly")...)
	if err == nil {
		t.Fatal("config init accepted a network that publishes no gRPC endpoint")
	}
	if !strings.Contains(err.Error(), "gRPC") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

// With nobody there to answer, an unreadable list has to stop the run. Silently
// falling back to the compiled devnet literals is the one outcome that hands back
// a real-looking wrong chain id.
func TestConfigInitFailsUnattendedWhenTheListIsUnreadable(t *testing.T) {
	g := newFixture(t)
	g.mustRun("", "wallet", "create", "--name", "guardian-one")
	g.unreachableRegistry()

	_, err := g.run("", setupArgs("guardian-one", g.dir)...)
	if err == nil {
		t.Fatal("config init completed unattended without resolving a network")
	}
	// The way out has to be in the message, or an air-gapped host has no route
	// through this command at all.
	if !strings.Contains(err.Error(), config.CustomNetworkID) {
		t.Errorf("the error does not name the escape hatch: %v", err)
	}
}

// --network custom is that escape hatch: the operator owns the endpoints, so
// there is nothing to read and no reason to require a list.
func TestConfigInitCustomNetworkReadsNoList(t *testing.T) {
	g := newFixture(t)
	g.mustRun("", "wallet", "create", "--name", "guardian-one")
	g.unreachableRegistry()

	g.mustRun("", append(setupArgs("guardian-one", g.dir), "--network", config.CustomNetworkID)...)

	if got := g.get("network"); got != config.CustomNetworkID {
		t.Errorf("network = %q, want %q", got, config.CustomNetworkID)
	}
	// The compiled defaults stand, and nothing inferred a transport for an
	// endpoint nobody stated one for.
	if got := g.get("chain-id"); got != "timeflare-test" {
		t.Errorf("chain_id = %q, want the default to stand", got)
	}
	if got := g.get("grpc-tls"); got != "false" {
		t.Errorf("grpc_tls = %q, want it left alone for a hand-configured endpoint", got)
	}
}

// The prompt is what an operator actually meets, so the numbering, the default
// and the escape hatch are worth asserting directly — the wizard around it needs
// a terminal, this does not.
func TestPromptNetwork(t *testing.T) {
	list, err := config.FetchNetworkList(t.Context(), writeRegistry(t))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("selects by number", func(t *testing.T) {
		choice, out := promptWith(t, list, "2\n")
		if choice.id != "testnet" {
			t.Errorf("selected %q, want testnet:\n%s", choice.id, out)
		}
		if !choice.grpcTLS {
			t.Error("a remote network was selected without TLS")
		}
	})

	t.Run("empty takes the published default", func(t *testing.T) {
		choice, out := promptWith(t, list, "\n")
		if choice.id != "devnet" {
			t.Errorf("selected %q, want the default devnet:\n%s", choice.id, out)
		}
	})

	t.Run("names an unusable network and does not offer it", func(t *testing.T) {
		_, out := promptWith(t, list, "1\n")
		if !strings.Contains(out, "restonly") {
			t.Errorf("the unusable network is not shown at all:\n%s", out)
		}
		if !strings.Contains(out, "unavailable") || !strings.Contains(out, "gRPC") {
			t.Errorf("the unusable network is shown without the reason:\n%s", out)
		}
		// Two usable networks, so custom is 3 — an unusable entry must not consume
		// a number an operator could type.
		if !strings.Contains(out, "3. Custom") {
			t.Errorf("custom is not numbered after the usable networks only:\n%s", out)
		}
	})

	t.Run("custom drops through to entering endpoints", func(t *testing.T) {
		choice, out := promptWith(t, list, "3\n")
		if choice.id != config.CustomNetworkID {
			t.Errorf("selected %q, want custom:\n%s", choice.id, out)
		}
		// Empty answers keep the defaults, and nothing guesses a transport.
		if choice.chainID != config.DefaultConfig().ChainID {
			t.Errorf("chain id is %q, want the default to stand", choice.chainID)
		}
		if choice.grpcTLS {
			t.Error("a hand-entered endpoint had TLS inferred for it")
		}
	})

	t.Run("refuses a number that is not on offer", func(t *testing.T) {
		var out bytes.Buffer
		if _, err := promptNetwork(ui.New(&out, strings.NewReader("9\n")), list); err == nil {
			t.Fatalf("accepted a selection that was never offered:\n%s", out.String())
		}
	})
}

func promptWith(t *testing.T, list *config.NetworkList, stdin string) (networkChoice, string) {
	t.Helper()
	var out bytes.Buffer
	choice, err := promptNetwork(ui.New(&out, strings.NewReader(stdin)), list)
	if err != nil {
		t.Fatalf("prompt failed on input %q: %v\n%s", stdin, err, out.String())
	}
	return choice, out.String()
}

// writeRegistry puts the shared test registry somewhere FetchNetworkList can read
// it, for the tests that work with the parsed list rather than through a command.
func writeRegistry(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "networks.json")
	if err := os.WriteFile(path, []byte(testRegistry), 0o600); err != nil {
		t.Fatalf("writing the test registry: %v", err)
	}
	return path
}
