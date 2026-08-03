package config

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/timeflareio/guardian/internal/custody"
)

// AddressPrefix is the bech32 account prefix for the chain. Single source for
// every address check in the guardian (the chain defines it in app.AccountAddressPrefix).
const AddressPrefix = "tmflr"

// Default path constants for guardian configuration and data files.
// These should only be used when no custom config path is specified.
const (
	// DefaultGuardianDir is the default directory for all guardian data
	DefaultGuardianDir = "$HOME/.timeflare/guardian"

	// DefaultConfigFileName is the default name for the config file
	DefaultConfigFileName = "config.yaml"

	// Default file names within the guardian directory
	DefaultPrivateKeyFileName = "private_key"
	DefaultPublicKeyFileName  = "public_key.hex"

	// DefaultConfigRelativePath is the full default config path
	DefaultConfigRelativePath = DefaultGuardianDir + "/" + DefaultConfigFileName
)

// Config is the single guardian configuration schema. Every parameter is a
// typed field carrying its own metadata via struct tags; the registry
// (registry.go) derives get/set/env/documentation from these tags, so a field
// that exists is wired by construction — there is no second struct and no
// hand-written conversion layer to drop values.
//
// Tag reference:
//
//	config: canonical key (kebab-case alt key and GUARDIAN_* env var derived)
//	group:  section heading for `config list` and the generated YAML
//	desc:   one-line description (shown in `config list` and the YAML comments)
//	path:   "true" → value is a filesystem path ($VAR/~ expanded on set)
type Config struct {
	// Network
	ChainID      string `config:"chain_id" group:"Network" desc:"Blockchain network identifier"`
	RPCEndpoint  string `config:"rpc_endpoint" group:"Network" desc:"CometBFT RPC endpoint (queries, event subscriptions)"`
	GRPCEndpoint string `config:"grpc_endpoint" group:"Network" desc:"gRPC endpoint (typed queries, transaction broadcast)"`

	// Identity & keys
	KeyName             string `config:"key_name" group:"Identity & Keys" desc:"Name of the guardian key in the keyring"`
	GuardianAddress     string `config:"guardian_address" group:"Identity & Keys" desc:"Guardian's blockchain address (resolved from key_name)"`
	GuardianID          string `config:"guardian_id" group:"Identity & Keys" desc:"Guardian identifier (defaults to key_name if empty)"`
	MonitorName         string `config:"monitor_name" group:"Identity & Keys" desc:"Human-readable name for monitoring/logs"`
	EncryptionPublicKey string `config:"encryption_public_key" group:"Identity & Keys" desc:"Guardian's encryption public key (64 hex chars = 32 bytes)"`

	EncryptionPrivateKeyPath string `config:"encryption_private_key_path" group:"Identity & Keys" desc:"Path to the private key file for share decryption" path:"true"`
	EncryptionKeyPassphrase  string `config:"encryption_key_passphrase" group:"Identity & Keys" desc:"Path to a file containing the share-key encryption passphrase, verbatim (falls back to the key file's sibling)" path:"true"`
	KeyringBackend           string `config:"keyring_backend" group:"Identity & Keys" desc:"Keyring backend type (file, os, test, memory)"`
	KeyringDir               string `config:"keyring_dir" group:"Identity & Keys" desc:"Directory holding the keyring" path:"true"`
	KeyringPassphrase        string `config:"keyring_passphrase" group:"Identity & Keys" desc:"Path to a file containing the keyring passphrase, verbatim (optional, for automation)" path:"true"`

	// Economics
	Denom            string  `config:"denom" group:"Economics" desc:"Base denomination for the network"`
	GasPrice         string  `config:"gas_price" group:"Economics" desc:"Gas price for transactions (e.g. 0.1uveil)"`
	GasAdjustment    float64 `config:"gas_adjustment" group:"Economics" desc:"Gas adjustment multiplier applied to simulation results"`
	StakeAmount      string  `config:"stake_amount" group:"Economics" desc:"Default float deposit for 'guardiand register' (flag-overridable)"`
	FeeBufferPercent int     `config:"fee_buffer_percent" group:"Economics" desc:"Balance headroom (percent of deposit) required beyond the deposit for fees"`

	// Chain interaction
	RequestTimeout time.Duration `config:"request_timeout" group:"Chain Interaction" desc:"Per-request timeout for chain queries and transactions"`
	RetryAttempts  int           `config:"retry_attempts" group:"Chain Interaction" desc:"Retry attempts for transient chain request failures"`
	RetryBackoff   time.Duration `config:"retry_backoff" group:"Chain Interaction" desc:"Base backoff between retries (linear per attempt)"`
	BlockTime      time.Duration `config:"block_time" group:"Chain Interaction" desc:"Expected block time — display maths and derived defaults only; consensus timing stays the chain's"`

	// Service
	PollingInterval      time.Duration `config:"polling_interval" group:"Service" desc:"Fallback poll rate (primary discovery is event-driven when enabled)"`
	MaxConcurrentSecrets int           `config:"max_concurrent_secrets" group:"Service" desc:"Maximum number of concurrent secret assignments"`
	MaxParallelReveals   int           `config:"max_parallel_reveals" group:"Service" desc:"Bounded parallelism for reveal submissions in one pass"`
	EnableHMACValidation bool          `config:"enable_hmac_validation" group:"Service" desc:"Enable HMAC validation for secret shares"`
	CacheMaxAge          time.Duration `config:"cache_max_age" group:"Service" desc:"Maximum age before a cached secret is evicted"`
	CacheCleanupInterval int64         `config:"cache_cleanup_interval" group:"Service" desc:"How often (in blocks) the secret cache runs cleanup"`
	ShutdownTimeout      time.Duration `config:"shutdown_timeout" group:"Service" desc:"Grace period for clean shutdown of all services"`

	// Event-driven operation
	EnableEventMonitoring bool          `config:"enable_event_monitoring" group:"Event Monitoring" desc:"React to chain events over WebSocket (polling remains the fallback)"`
	EventReconnectBackoff time.Duration `config:"event_reconnect_backoff" group:"Event Monitoring" desc:"Backoff before reconnecting a dropped event subscription"`
	RevealOffsetBlocks    int64         `config:"reveal_offset_blocks" group:"Event Monitoring" desc:"Max random block offset after window-open before revealing (0 = reveal immediately)"`

	// Monitoring & observability
	BindAddress   string `config:"bind_address" group:"Monitoring" desc:"Bind address for the metrics and health listeners"`
	MetricsPort   int    `config:"metrics_port" group:"Monitoring" desc:"Prometheus metrics endpoint port"`
	HealthPort    int    `config:"health_port" group:"Monitoring" desc:"Health check endpoint port"`
	EnableMetrics bool   `config:"enable_metrics" group:"Monitoring" desc:"Serve the Prometheus metrics endpoint"`
	// Dashboard: the operator's read-only page, on the shared BindAddress
	// alongside health and metrics.
	//
	// The page names bond exposure, key fingerprints, the encrypted-at-rest
	// status (including the plaintext-key warning) and the full config, so
	// beyond loopback it needs a credential: dashboard_password_hash holds a
	// bcrypt hash and the dashboard is not served without one on a non-loopback
	// bind address. The hash is not a secret — a leaked config file yields an
	// offline attack against it, which is why 'set-dashboard-password
	// --generate' exists.
	//
	// The rule keys off the bind address, which under Docker is not a proxy for
	// exposure: a container binds 0.0.0.0 for -p to publish anything, and the
	// daemon cannot see what was published. Erring towards an unserved page is
	// the deliberate choice — the alternative is an operator-asserted "not
	// exposed" flag that can be set once and forgotten.
	EnableDashboard bool `config:"enable_dashboard" group:"Monitoring" desc:"Serve the read-only operator dashboard (needs dashboard_password_hash beyond loopback)"`
	DashboardPort   int  `config:"dashboard_port" group:"Monitoring" desc:"Operator dashboard port"`
	// Displayed as set/not set rather than as the hash itself: a 60-character
	// blob in an operator's config report is noise, not a disclosure risk.
	DashboardPasswordHash string `config:"dashboard_password_hash" group:"Monitoring" desc:"bcrypt hash of the dashboard password (set it with 'guardiand config set-dashboard-password')" display:"presence"`
	// TLS for the dashboard listener alone; health and metrics are unaffected.
	// Both or neither. Without it Basic auth defends against unauthorised
	// readers but not against a network eavesdropper — base64 is not
	// encryption, and the credential crosses the network on every poll.
	DashboardTLSCertFile string `config:"dashboard_tls_cert_file" group:"Monitoring" desc:"PEM certificate serving the dashboard over TLS (with dashboard_tls_key_file)" path:"true"`
	DashboardTLSKeyFile  string `config:"dashboard_tls_key_file" group:"Monitoring" desc:"PEM private key serving the dashboard over TLS (with dashboard_tls_cert_file)" path:"true"`
	EnableHealthCheck    bool   `config:"enable_health_check" group:"Monitoring" desc:"Serve the health/readiness endpoints"`
	LogLevel             string `config:"log_level" group:"Monitoring" desc:"Logging level (debug, info, warn, error)"`
	LogFormat            string `config:"log_format" group:"Monitoring" desc:"Log output format (console, json)"`
	LogFilePath          string `config:"log_file_path" group:"Monitoring" desc:"Log file path (empty = stderr)" path:"true"`

	// Lazy loading state (not serialised). Held behind a pointer so Config
	// values can be copied (Manager.GetConfig) while sharing one key cache.
	keyCache *encryptionKeyCache `config:"-"`
}

// encryptionKeyCache lazily holds the decrypted share-encryption private key
// (the current epoch's) plus any retired epoch keys loaded to serve
// assignments encrypted under earlier epochs (key rotation).
type encryptionKeyCache struct {
	mu        sync.RWMutex
	key       *[32]byte
	epochKeys map[uint64]*[32]byte
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		keyCache: &encryptionKeyCache{},

		ChainID:      "timeflare-test",
		RPCEndpoint:  "http://localhost:26657",
		GRPCEndpoint: "localhost:9090",

		KeyName:             "",
		GuardianAddress:     "",
		GuardianID:          "",
		MonitorName:         "Timeflare Guardian",
		EncryptionPublicKey: "",

		EncryptionPrivateKeyPath: expandPath(DefaultGuardianDir + "/" + DefaultPrivateKeyFileName),
		EncryptionKeyPassphrase:  "",
		KeyringBackend:           "file",
		KeyringDir:               expandPath(DefaultGuardianDir),
		KeyringPassphrase:        "",

		Denom:            "uveil",
		GasPrice:         "0.1uveil",
		GasAdjustment:    1.5,
		StakeAmount:      "10000000000uveil",
		FeeBufferPercent: 1,

		RequestTimeout: 30 * time.Second,
		RetryAttempts:  3,
		RetryBackoff:   2 * time.Second,
		BlockTime:      6 * time.Second,

		PollingInterval:      6 * time.Second,
		MaxConcurrentSecrets: 100,
		MaxParallelReveals:   4,
		EnableHMACValidation: true,
		CacheMaxAge:          7 * 24 * time.Hour,
		CacheCleanupInterval: 50,
		ShutdownTimeout:      30 * time.Second,

		EnableEventMonitoring: true,
		EventReconnectBackoff: 5 * time.Second,
		RevealOffsetBlocks:    0,

		BindAddress: "0.0.0.0",
		// 21000/21100 rather than the conventional 8080/9100: 9100 is
		// node_exporter's canonical port and 8080 is the most contested port on
		// any host, so the defaults sat where an operator's existing monitoring
		// already lives. The 21000–21199 region is above 1024 (no privileged
		// bind), below the ephemeral floor (49152 on macOS/BSD, 32768 on Linux)
		// so a collision can never be a random one, and outside Kubernetes'
		// 30000–32767 NodePort range. The devnet fans out from the same bases
		// (guardian i takes 21000+i / 21100+i — devnet/guardians.sh).
		MetricsPort:     21100,
		HealthPort:      21000,
		EnableMetrics:   true,
		EnableDashboard: true,
		DashboardPort:   21200,
		// Empty by default, so a guardian binding beyond loopback does not
		// serve the dashboard until an operator sets a credential.
		DashboardPasswordHash: "",
		DashboardTLSCertFile:  "",
		DashboardTLSKeyFile:   "",
		EnableHealthCheck:     true,
		LogLevel:              "info",
		LogFormat:             "console",
		LogFilePath:           "",
	}
}

// Validate checks the configuration for completeness and cross-field
// consistency. Field-level parse errors are caught at set time by the
// registry; this covers everything a single field can't see.
func (cfg *Config) Validate() error {
	if cfg.ChainID == "" {
		return fmt.Errorf("chain_id is required")
	}
	if cfg.KeyName == "" {
		return fmt.Errorf("key_name is required")
	}
	if cfg.RPCEndpoint == "" {
		return fmt.Errorf("rpc_endpoint is required")
	}
	if cfg.GRPCEndpoint == "" {
		return fmt.Errorf("grpc_endpoint is required")
	}
	if cfg.Denom == "" {
		return fmt.Errorf("denom is required")
	}

	if cfg.GuardianAddress != "" {
		if err := ValidateGuardianAddress(cfg.GuardianAddress); err != nil {
			return fmt.Errorf("invalid guardian_address: %w", err)
		}
	}

	if cfg.EncryptionPublicKey != "" && len(cfg.EncryptionPublicKey) != 64 {
		return fmt.Errorf("encryption_public_key must be exactly 64 hex characters (32 bytes)")
	}

	// Ports: valid, distinct, and not colliding with the gRPC endpoint's port
	// on the same host (default metrics 9090 vs grpc localhost:9090 was broken
	// out of the box before this check existed).
	if cfg.MetricsPort <= 0 || cfg.MetricsPort > 65535 {
		return fmt.Errorf("metrics_port must be between 1 and 65535")
	}
	if cfg.HealthPort <= 0 || cfg.HealthPort > 65535 {
		return fmt.Errorf("health_port must be between 1 and 65535")
	}
	if cfg.MetricsPort == cfg.HealthPort {
		return fmt.Errorf("metrics_port and health_port cannot be the same")
	}
	if cfg.DashboardPort <= 0 || cfg.DashboardPort > 65535 {
		return fmt.Errorf("dashboard_port must be between 1 and 65535")
	}
	// Three listeners now, so distinctness is checked pairwise rather than
	// once: two of them sharing a port fails at bind time with a message about
	// whichever lost the race, which is a poor way to learn about a typo.
	if cfg.DashboardPort == cfg.MetricsPort {
		return fmt.Errorf("dashboard_port and metrics_port cannot be the same")
	}
	if cfg.DashboardPort == cfg.HealthPort {
		return fmt.Errorf("dashboard_port and health_port cannot be the same")
	}
	if p := endpointPort(cfg.GRPCEndpoint); p != "" {
		if fmt.Sprintf("%d", cfg.MetricsPort) == p {
			return fmt.Errorf("metrics_port %d collides with grpc_endpoint %s", cfg.MetricsPort, cfg.GRPCEndpoint)
		}
		if fmt.Sprintf("%d", cfg.HealthPort) == p {
			return fmt.Errorf("health_port %d collides with grpc_endpoint %s", cfg.HealthPort, cfg.GRPCEndpoint)
		}
		if fmt.Sprintf("%d", cfg.DashboardPort) == p {
			return fmt.Errorf("dashboard_port %d collides with grpc_endpoint %s", cfg.DashboardPort, cfg.GRPCEndpoint)
		}
	}

	// A half-configured pair would otherwise fail at listener setup, long after
	// the operator typed the one path they remembered.
	if (cfg.DashboardTLSCertFile == "") != (cfg.DashboardTLSKeyFile == "") {
		return fmt.Errorf("dashboard_tls_cert_file and dashboard_tls_key_file must be set together")
	}
	if cfg.DashboardPasswordHash != "" {
		if err := ValidatePasswordHash(cfg.DashboardPasswordHash); err != nil {
			return fmt.Errorf("invalid dashboard_password_hash: %w", err)
		}
	}

	if !isValidLogLevel(cfg.LogLevel) {
		return fmt.Errorf("log_level must be one of: debug, info, warn, error")
	}
	if !isValidLogFormat(cfg.LogFormat) {
		return fmt.Errorf("log_format must be one of: console, json")
	}

	if cfg.MaxConcurrentSecrets <= 0 {
		return fmt.Errorf("max_concurrent_secrets must be positive")
	}
	if cfg.MaxParallelReveals <= 0 {
		return fmt.Errorf("max_parallel_reveals must be positive")
	}
	if cfg.PollingInterval <= 0 {
		return fmt.Errorf("polling_interval must be positive")
	}
	if cfg.BlockTime <= 0 {
		return fmt.Errorf("block_time must be positive")
	}
	if cfg.PollingInterval < cfg.BlockTime/2 {
		return fmt.Errorf("polling_interval (%s) is more than twice as fast as block_time (%s) — pointless load", cfg.PollingInterval, cfg.BlockTime)
	}
	if cfg.RequestTimeout <= 0 {
		return fmt.Errorf("request_timeout must be positive")
	}
	if cfg.RetryAttempts <= 0 {
		return fmt.Errorf("retry_attempts must be positive")
	}
	if cfg.RevealOffsetBlocks < 0 {
		return fmt.Errorf("reveal_offset_blocks cannot be negative")
	}

	return nil
}

// ValidateGuardianAddress checks a bech32 account address against the chain's
// prefix using the cosmos bech32 decoder (no hand-rolled charset checks).
func ValidateGuardianAddress(address string) error {
	if address == "" {
		return fmt.Errorf("address cannot be empty")
	}
	hrp, data, err := bech32.DecodeAndConvert(address)
	if err != nil {
		return fmt.Errorf("not a valid bech32 address: %w", err)
	}
	if hrp != AddressPrefix {
		return fmt.Errorf("address prefix must be %q, got %q", AddressPrefix, hrp)
	}
	if len(data) != 20 {
		return fmt.Errorf("address payload must be 20 bytes, got %d", len(data))
	}
	return nil
}

// GetEncryptionPrivateKey loads and caches the private key, loading from file
// only once. Both key formats load transparently: the encrypted envelope
// (default; passphrase from the configured file or the key's sibling
// encryption_key_passphrase file) and the legacy raw 32-byte plaintext file.
//
// Custody trade-off (guardian key custody plan, Phase 1): the decrypted key
// stays cached for the process lifetime — the daemon decrypts shares
// continuously, so re-deriving the KEK per use would buy nothing against a
// same-privilege attacker while costing an argon2id pass per reveal. The
// cache is wiped on shutdown (WipeEncryptionKey); the encryption defends
// backups and at-rest theft, not a compromised live process.
func (cfg *Config) GetEncryptionPrivateKey() ([32]byte, error) {
	cache := cfg.keyCache

	cache.mu.RLock()
	if cache.key != nil {
		key := *cache.key
		cache.mu.RUnlock()
		return key, nil
	}
	cache.mu.RUnlock()

	cache.mu.Lock()
	defer cache.mu.Unlock()

	// Double-check pattern
	if cache.key != nil {
		return *cache.key, nil
	}

	// Load from file (encrypted envelope or legacy plaintext)
	key, err := custody.LoadShareKey(cfg.EncryptionPrivateKeyPath,
		custody.FilePassphrase(cfg.EncryptionKeyPassphrase, cfg.EncryptionPrivateKeyPath))
	if err != nil {
		return [32]byte{}, err
	}

	cache.key = &key
	return key, nil
}

// GetRetiredEpochKey loads and caches a RETIRED epoch's private key from its
// conventional on-disk location (<private_key_path>.epoch<N>, same envelope
// format and passphrase resolution as the current key). Used by the epoch
// keyring to decrypt assignments made under earlier key epochs; the current
// epoch's key is served by GetEncryptionPrivateKey.
func (cfg *Config) GetRetiredEpochKey(epoch uint64) ([32]byte, error) {
	cache := cfg.keyCache

	cache.mu.RLock()
	if cached, ok := cache.epochKeys[epoch]; ok {
		key := *cached
		cache.mu.RUnlock()
		return key, nil
	}
	cache.mu.RUnlock()

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cached, ok := cache.epochKeys[epoch]; ok {
		return *cached, nil
	}

	path := custody.EpochKeyPath(cfg.EncryptionPrivateKeyPath, epoch)
	key, err := custody.LoadShareKey(path,
		custody.FilePassphrase(cfg.EncryptionKeyPassphrase, cfg.EncryptionPrivateKeyPath))
	if err != nil {
		return [32]byte{}, err
	}

	if cache.epochKeys == nil {
		cache.epochKeys = make(map[uint64]*[32]byte)
	}
	cache.epochKeys[epoch] = &key
	return key, nil
}

// HasRetiredEpochKeyFile reports whether the retired epoch's key file exists
// on disk (without decrypting it).
func (cfg *Config) HasRetiredEpochKeyFile(epoch uint64) bool {
	_, err := os.Stat(custody.EpochKeyPath(cfg.EncryptionPrivateKeyPath, epoch))
	return err == nil
}

// WipeEncryptionKey zeroes and drops every cached private key — current and
// retired epochs (called on shutdown, and when a rotation event invalidates
// the on-disk layout; the next Get* reloads from file).
func (cfg *Config) WipeEncryptionKey() {
	cfg.keyCache.mu.Lock()
	defer cfg.keyCache.mu.Unlock()
	if cfg.keyCache.key != nil {
		custody.Zero(cfg.keyCache.key[:])
		cfg.keyCache.key = nil
	}
	for epoch, key := range cfg.keyCache.epochKeys {
		custody.Zero(key[:])
		delete(cfg.keyCache.epochKeys, epoch)
	}
}

// SetEncryptionPrivateKey pre-populates the key cache (used by tests and by
// flows that generate the key in-process).
func (cfg *Config) SetEncryptionPrivateKey(key [32]byte) {
	cfg.keyCache.mu.Lock()
	defer cfg.keyCache.mu.Unlock()
	cfg.keyCache.key = &key
}

// EffectiveGuardianID returns guardian_id, defaulting to key_name.
func (cfg *Config) EffectiveGuardianID() string {
	if cfg.GuardianID == "" {
		return cfg.KeyName
	}
	return cfg.GuardianID
}

func isValidLogLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	}
	return false
}

func isValidLogFormat(format string) bool {
	return format == "console" || format == "json"
}

// endpointPort extracts the port of a host:port endpoint ("" if none).
func endpointPort(endpoint string) string {
	for i := len(endpoint) - 1; i >= 0; i-- {
		if endpoint[i] == ':' {
			return endpoint[i+1:]
		}
		if endpoint[i] < '0' || endpoint[i] > '9' {
			return ""
		}
	}
	return ""
}
