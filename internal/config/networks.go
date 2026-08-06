package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// The chain publishes the networks it runs as — chain id, endpoints, and whether
// the deployment is loopback-scoped — as one entry per network. A guardian is
// pointed at a network by choosing from that list, rather than by an operator
// typing three related strings of which the chain id is both the hardest to
// guess and the least forgiving: get it wrong and queries still work while every
// transaction fails.
//
// This is read in exactly one place, `guardianctl config init`. What selection
// writes into the configuration is what the daemon runs on, so nothing at
// runtime depends on the list being reachable and a guardian already configured
// never consults it.

const (
	// NetworkListURL is where the chain publishes the list. It will move to a
	// TLS-served path on a timeflare.io domain, and this constant is the one
	// place that changes when it does.
	NetworkListURL = "https://raw.githubusercontent.com/timeflareio/chain/main/networks.json"

	// NetworkListURLEnv overrides NetworkListURL and accepts a filesystem path
	// as readily as a URL. The chain's devnet points it at its own checkout's
	// networks.json, so dev-up and the e2e suites exercise selection against the
	// file the chain owns instead of depending on reaching GitHub.
	NetworkListURLEnv = "GUARDIAN_NETWORK_LIST_URL"

	// NetworkListTimeout bounds the read, and there is no retry. Setup either
	// has a list to offer or says so and moves on; it never hangs waiting for
	// one.
	NetworkListTimeout = 5 * time.Second

	// CustomNetworkID is what the network key records when the endpoints were
	// typed rather than selected. It is this daemon's own and never the chain's:
	// a private node, or a network the published list does not carry, has to
	// remain configurable.
	CustomNetworkID = "custom"

	// networkListMaxBytes bounds what is read from a host this daemon does not
	// operate.
	networkListMaxBytes = 1 << 20
)

// RegistryEndpoints is a network's endpoints as published. Each is a list so
// that one unreachable host is not an unreachable network.
//
// REST is read only so this type mirrors the published shape rather than a
// guardian-shaped subset of it: this daemon speaks native gRPC and CometBFT RPC
// and has no REST client.
type RegistryEndpoints struct {
	RPC  []string `json:"rpc"`
	REST []string `json:"rest"`
	GRPC []string `json:"grpc"`
}

// RegistryNetwork is one network as the chain defines it.
type RegistryNetwork struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	ChainID string `json:"chainId"`
	// Local is loopback-scoped: reachable only from the machine running the
	// node, which is the one place the registry permits cleartext. Held behind a
	// pointer so an entry that omits it is a malformed entry rather than one
	// silently reading as non-local.
	Local *bool `json:"local"`
	// BlockTime is the cadence the network runs at, if the registry states one.
	// Deployment fact rather than protocol: every window the protocol defines is
	// a block count. Read at `config init` to size the fallback poll rate, and
	// deliberately not stored in the daemon's config — nothing it does should
	// depend on knowing this.
	BlockTime string            `json:"blockTime"`
	Endpoints RegistryEndpoints `json:"endpoints"`
}

// NetworkList is the published document.
type NetworkList struct {
	Default       string            `json:"default"`
	AddressPrefix string            `json:"addressPrefix"`
	Networks      []RegistryNetwork `json:"networks"`
}

// IsLocal reports whether the network is loopback-scoped.
func (n RegistryNetwork) IsLocal() bool {
	return n.Local != nil && *n.Local
}

// GRPCTLS reports whether a gRPC dial to this network needs TLS.
//
// An rpc or rest URL states its own transport in its scheme; a gRPC address is
// host:port with nowhere to put one, and a gRPC client has to choose between
// transport credentials and an insecure dial before it connects. Locality is
// what decides, and it decides by rule rather than by correlation: the chain's
// verify-networks requires a local entry's URLs to be loopback and a non-local
// entry's to be https, so the registry cannot publish a network where the two
// diverge.
func (n RegistryNetwork) GRPCTLS() bool {
	return !n.IsLocal()
}

// RPCEndpoint returns the endpoint to configure, or "" if the network publishes
// none.
func (n RegistryNetwork) RPCEndpoint() string {
	return first(n.Endpoints.RPC)
}

// GRPCEndpoint returns the endpoint to configure, or "" if the network publishes
// none.
func (n RegistryNetwork) GRPCEndpoint() string {
	return first(n.Endpoints.GRPC)
}

// Unusable explains why this daemon cannot run against the network, or "" when
// it can.
//
// An empty endpoint list is a statement rather than an omission — plenty of
// deployments expose REST and not gRPC — and a daemon that needs gRPC should
// learn at configuration time that it cannot run against such a network instead
// of at its first dial. Such a network stays listed, carrying this reason, so an
// operator told to join it learns it was seen and rejected.
func (n RegistryNetwork) Unusable() string {
	switch {
	case n.GRPCEndpoint() == "":
		return "publishes no gRPC endpoint, which this daemon requires for queries and broadcast"
	case n.RPCEndpoint() == "":
		return "publishes no CometBFT RPC endpoint, which this daemon requires for events and block reads"
	default:
		return ""
	}
}

// Find resolves a network by id.
func (l *NetworkList) Find(id string) (RegistryNetwork, bool) {
	for _, n := range l.Networks {
		if n.ID == id {
			return n, true
		}
	}
	return RegistryNetwork{}, false
}

// DefaultNetwork resolves the entry the list names as its default, which is what
// a guardian gets when the operator names no network. Validate guarantees it
// resolves.
func (l *NetworkList) DefaultNetwork() (RegistryNetwork, bool) {
	return l.Find(l.Default)
}

// IDs lists every published id, for naming the choices back to an operator who
// asked for one that does not exist.
func (l *NetworkList) IDs() []string {
	ids := make([]string, 0, len(l.Networks))
	for _, n := range l.Networks {
		ids = append(ids, n.ID)
	}
	return ids
}

// Validate checks what this daemon reads and nothing more.
//
// Deliberately shallow. A list carrying a field this build does not understand
// is a newer chain talking to an older guardianctl, which is the ordinary case
// over time rather than an error, so unknown fields are ignored. The endpoints
// being array-valued is enforced by the decoder — a scalar there fails the
// unmarshal — and whether a given network has the endpoints this daemon needs is
// Unusable's judgement, made where it is acted on rather than rejecting the
// whole list over one entry.
func (l *NetworkList) Validate() error {
	if l.Default == "" {
		return fmt.Errorf("no default network named")
	}
	if len(l.Networks) == 0 {
		return fmt.Errorf("no networks listed")
	}
	for i, n := range l.Networks {
		switch {
		case n.ID == "":
			return fmt.Errorf("network %d has no id", i)
		case n.Label == "":
			return fmt.Errorf("network %q has no label", n.ID)
		case n.ChainID == "":
			return fmt.Errorf("network %q has no chain id", n.ID)
		case n.Local == nil:
			return fmt.Errorf("network %q does not say whether it is local", n.ID)
		}
	}
	// Checked last so the message names a fault in the default rather than a
	// fault in the list, which is what an operator can act on.
	if _, ok := l.DefaultNetwork(); !ok {
		return fmt.Errorf("default %q names no listed network", l.Default)
	}
	return nil
}

// NetworkListSource returns where to read the list from: the environment
// override when set, otherwise the published URL.
func NetworkListSource() string {
	if override := strings.TrimSpace(os.Getenv(NetworkListURLEnv)); override != "" {
		return override
	}
	return NetworkListURL
}

// FetchNetworkList reads and validates the list. The source is a parameter
// rather than read from NetworkListSource here so tests drive it against a local
// server or file and none of them touch the network.
//
// The caller's context may shorten NetworkListTimeout but never extend it.
func FetchNetworkList(ctx context.Context, source string) (*NetworkList, error) {
	ctx, cancel := context.WithTimeout(ctx, NetworkListTimeout)
	defer cancel()

	var (
		body []byte
		err  error
	)
	if isHTTPSource(source) {
		body, err = readOverHTTP(ctx, source)
	} else {
		body, err = os.ReadFile(source)
	}
	if err != nil {
		return nil, fmt.Errorf("could not read the network list from %s: %w", source, err)
	}

	var list NetworkList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("%s is not a usable network list: %w", source, err)
	}
	if err := list.Validate(); err != nil {
		return nil, fmt.Errorf("%s is not a usable network list: %w", source, err)
	}
	return &list, nil
}

// isHTTPSource distinguishes a URL from a filesystem path, so the environment
// override can carry either.
func isHTTPSource(source string) bool {
	u, err := url.Parse(source)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func readOverHTTP(ctx context.Context, source string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("responded %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, networkListMaxBytes))
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
