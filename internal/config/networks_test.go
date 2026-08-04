package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A guardian takes its chain id and endpoints from this list, and a chain id is
// the value whose failure mode hides: queries keep working while every
// transaction fails. So the reader has to reject a list it cannot trust rather
// than silently configure a guardian from half of one.

const validList = `{
  "default": "devnet",
  "addressPrefix": "tmflr",
  "networks": [
    {
      "id": "devnet", "label": "Local devnet", "chainId": "timeflare-test", "local": true,
      "endpoints": {"rpc": ["http://localhost:26657"], "rest": ["http://localhost:1317"], "grpc": ["localhost:9090"]}
    },
    {
      "id": "testnet", "label": "Public testnet", "chainId": "timeflare-testnet-1", "local": false,
      "endpoints": {"rpc": ["https://rpc.example.org"], "rest": ["https://api.example.org"], "grpc": ["grpc.example.org:443"]}
    }
  ]
}`

func TestNetworkListValidation(t *testing.T) {
	for _, c := range []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "well formed",
			body: validList,
		},
		{
			// A default naming nothing would leave an unattended setup with no
			// network at all, which is the one case that must not resolve to the
			// compiled devnet literals.
			name:    "default names no network",
			body:    `{"default":"mainnet","networks":[{"id":"devnet","label":"L","chainId":"c","local":true,"endpoints":{"rpc":["r"],"grpc":["g"]}}]}`,
			wantErr: `default "mainnet" names no listed network`,
		},
		{
			name:    "no default",
			body:    `{"networks":[{"id":"devnet","label":"L","chainId":"c","local":true,"endpoints":{"rpc":["r"],"grpc":["g"]}}]}`,
			wantErr: "no default network named",
		},
		{
			name:    "no networks",
			body:    `{"default":"devnet","networks":[]}`,
			wantErr: "no networks listed",
		},
		{
			name:    "entry without a chain id",
			body:    `{"default":"devnet","networks":[{"id":"devnet","label":"L","local":true,"endpoints":{"rpc":["r"],"grpc":["g"]}}]}`,
			wantErr: `network "devnet" has no chain id`,
		},
		{
			// Absent is not false. A missing local would otherwise read as
			// "remote" and decide the transport, which is not a decision to make
			// from a field that was never written.
			name:    "entry that does not say whether it is local",
			body:    `{"default":"devnet","networks":[{"id":"devnet","label":"L","chainId":"c","endpoints":{"rpc":["r"],"grpc":["g"]}}]}`,
			wantErr: `network "devnet" does not say whether it is local`,
		},
		{
			// The decoder enforces the shape, so a scalar where a list belongs is
			// a list this build cannot read rather than one it half-reads.
			name:    "endpoints that are not lists",
			body:    `{"default":"devnet","networks":[{"id":"devnet","label":"L","chainId":"c","local":true,"endpoints":{"rpc":"http://localhost:26657","grpc":["g"]}}]}`,
			wantErr: "not a usable network list",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := FetchNetworkList(context.Background(), writeList(t, c.body))
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("rejected a usable list: %v", err)
			case c.wantErr == "":
				return
			case err == nil:
				t.Fatalf("accepted a list that should have failed with %q", c.wantErr)
			case !strings.Contains(err.Error(), c.wantErr):
				t.Errorf("error was %q, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

// A field this build does not know is a newer chain talking to an older
// guardianctl, which is the ordinary case over time — not a reason to refuse to
// configure a guardian.
func TestNetworkListAcceptsUnknownFields(t *testing.T) {
	body := `{
      "default": "devnet", "addressPrefix": "tmflr", "somethingNew": {"nested": true},
      "networks": [{
        "id": "devnet", "label": "L", "chainId": "timeflare-test", "local": true, "futureFlag": "x",
        "endpoints": {"rpc": ["http://localhost:26657"], "rest": ["http://localhost:1317"], "grpc": ["localhost:9090"], "somethingElse": ["y"]}
      }]
    }`
	list, err := FetchNetworkList(context.Background(), writeList(t, body))
	if err != nil {
		t.Fatalf("a list with unknown fields was rejected: %v", err)
	}
	if got := list.Default; got != "devnet" {
		t.Errorf("default is %q, want devnet", got)
	}
}

// Locality decides the transport, because a gRPC address has no scheme to state
// one and the chain ties the two together in its own checks.
func TestGRPCTLSFollowsLocality(t *testing.T) {
	list, err := FetchNetworkList(context.Background(), writeList(t, validList))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id      string
		wantTLS bool
	}{
		{"devnet", false},
		{"testnet", true},
	} {
		n, ok := list.Find(c.id)
		if !ok {
			t.Fatalf("%s is missing from the list", c.id)
		}
		if got := n.GRPCTLS(); got != c.wantTLS {
			t.Errorf("%s: GRPCTLS() = %v, want %v", c.id, got, c.wantTLS)
		}
	}
}

// An empty gRPC list is a statement, and the daemon needs to learn it here
// rather than at its first dial.
func TestNetworkWithoutGRPCIsUnusable(t *testing.T) {
	body := `{"default":"devnet","networks":[
      {"id":"devnet","label":"L","chainId":"timeflare-test","local":true,"endpoints":{"rpc":["http://localhost:26657"],"grpc":["localhost:9090"]}},
      {"id":"restonly","label":"R","chainId":"timeflare-restonly","local":false,"endpoints":{"rpc":["https://r.example.org"],"grpc":[]}}
    ]}`
	list, err := FetchNetworkList(context.Background(), writeList(t, body))
	if err != nil {
		t.Fatal(err)
	}

	usable, _ := list.Find("devnet")
	if reason := usable.Unusable(); reason != "" {
		t.Errorf("devnet reported unusable: %s", reason)
	}
	restOnly, _ := list.Find("restonly")
	if reason := restOnly.Unusable(); reason == "" {
		t.Error("a network publishing no gRPC endpoint was reported as usable")
	} else if !strings.Contains(reason, "gRPC") {
		t.Errorf("the reason does not name the missing endpoint: %q", reason)
	}
}

func TestFetchNetworkListOverHTTP(t *testing.T) {
	t.Run("serves the list", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(validList))
		}))
		defer server.Close()

		list, err := FetchNetworkList(context.Background(), server.URL)
		if err != nil {
			t.Fatalf("reading a served list: %v", err)
		}
		if _, ok := list.DefaultNetwork(); !ok {
			t.Error("the default does not resolve")
		}
	})

	t.Run("reports a non-200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		_, err := FetchNetworkList(context.Background(), server.URL)
		if err == nil {
			t.Fatal("a 404 was accepted as a network list")
		}
		if !strings.Contains(err.Error(), "404") {
			t.Errorf("error does not name the status: %v", err)
		}
	})

	t.Run("gives up rather than hanging", func(t *testing.T) {
		// The caller's deadline shortens NetworkListTimeout, which is what keeps
		// this test quick while proving the bound exists.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, err := FetchNetworkList(ctx, server.URL); err == nil {
			t.Fatal("a hanging server was accepted as a network list")
		}
		if elapsed := time.Since(start); elapsed >= NetworkListTimeout {
			t.Errorf("waited %s, which is the full timeout rather than the caller's", elapsed)
		}
	})
}

// The override carries a path as readily as a URL, which is what keeps the devnet
// and this suite off the network.
func TestNetworkListSourcePrefersTheOverride(t *testing.T) {
	if got := NetworkListSource(); got != NetworkListURL {
		t.Errorf("with no override the source is %q, want the published URL", got)
	}
	t.Setenv(NetworkListURLEnv, "/tmp/networks.json")
	if got := NetworkListSource(); got != "/tmp/networks.json" {
		t.Errorf("the override was ignored: %q", got)
	}
}

func writeList(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "networks.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
