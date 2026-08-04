package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timeflareio/guardian/internal/custody"
)

// validTestAddress builds a real bech32 address with the chain prefix.
func validTestAddress(t *testing.T) string {
	t.Helper()
	addr, err := bech32.ConvertAndEncode(AddressPrefix, []byte("12345678901234567890"))
	require.NoError(t, err)
	return addr
}

func TestValidateGuardianAddress(t *testing.T) {
	valid := validTestAddress(t)

	tests := []struct {
		name          string
		guardianAddr  string
		expectedError string
	}{
		{name: "empty", guardianAddr: "", expectedError: "address cannot be empty"},
		{name: "not bech32", guardianAddr: "tmflr1notbech32!!!", expectedError: "not a valid bech32 address"},
		{name: "wrong prefix", guardianAddr: "cosmos1qypqxpq9qcrsszg2pvxq6rs0zqg3yyc5lzv7xu", expectedError: "prefix must be"},
		{name: "valid", guardianAddr: valid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGuardianAddress(tt.guardianAddr)
			if tt.expectedError == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			}
		})
	}
}

// TestConfigFieldRoundTrip is the §1 regression test: every registered key
// must survive set → save → load with its effective (typed) value intact.
// polling_interval famously did not — ToGuardianConfig() hard-coded 6s.
func TestConfigFieldRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	m := NewManager(path)
	require.NoError(t, m.Set("key_name", "test-guardian"))
	require.NoError(t, m.Set("polling-interval", "2s")) // kebab alt key
	require.NoError(t, m.Set("retry_attempts", "7"))
	require.NoError(t, m.Set("enable_metrics", "false"))
	require.NoError(t, m.Set("log_file_path", "/tmp/guardian.log"))
	require.NoError(t, m.Set("gas_adjustment", "2.5"))
	require.NoError(t, m.Set("cache_max_age", "48h"))
	require.NoError(t, m.Save())

	loaded := NewManager(path)
	require.NoError(t, loaded.Load())
	cfg := loaded.GetConfig()

	// Effective values as the running service consumes them — typed fields,
	// not registry strings.
	assert.Equal(t, 2*time.Second, cfg.PollingInterval)
	assert.Equal(t, 7, cfg.RetryAttempts)
	assert.False(t, cfg.EnableMetrics)
	assert.Equal(t, "/tmp/guardian.log", cfg.LogFilePath)
	assert.Equal(t, 2.5, cfg.GasAdjustment)
	assert.Equal(t, 48*time.Hour, cfg.CacheMaxAge)
	assert.Equal(t, "test-guardian", cfg.KeyName)
}

func TestEveryFieldRegisteredAndRoundTrips(t *testing.T) {
	// Every registered key must Get and re-Set with its own output — catches
	// type/format mismatches for any newly added field automatically.
	cfg := DefaultConfig()
	for _, key := range Keys() {
		value, err := cfg.GetField(key)
		require.NoError(t, err, key)
		require.NoError(t, cfg.SetField(key, value), key)
		after, err := cfg.GetField(key)
		require.NoError(t, err, key)
		assert.Equal(t, value, after, key)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("GUARDIAN_POLLING_INTERVAL", "3s")
	t.Setenv("GUARDIAN_MAX_PARALLEL_REVEALS", "9")

	m := NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, m.LoadOrDefault())
	cfg := m.GetConfig()

	assert.Equal(t, 3*time.Second, cfg.PollingInterval)
	assert.Equal(t, 9, cfg.MaxParallelReveals)
}

func TestEnvOverridesApplyOnTopOfFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := NewManager(path)
	require.NoError(t, m.Set("polling_interval", "10s"))
	require.NoError(t, m.Set("log_level", "debug"))
	require.NoError(t, m.Save())

	t.Setenv("GUARDIAN_POLLING_INTERVAL", "1s")

	loaded := NewManager(path)
	require.NoError(t, loaded.Load())
	cfg := loaded.GetConfig()

	assert.Equal(t, 1*time.Second, cfg.PollingInterval, "env overrides file")
	assert.Equal(t, "debug", cfg.LogLevel, "file value survives where no env is set")
}

func TestUnknownKeyRejected(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	assert.Error(t, m.Set("timeflared_binary", "/usr/bin/timeflared"), "retired key must be unknown")
	_, err := m.Get("no_such_key")
	assert.Error(t, err)
}

func TestUnknownKeysInFileIgnored(t *testing.T) {
	// Old config files carry retired keys (timeflared_binary); loading must
	// not fail on them.
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("key_name: g1\ntimeflared_binary: timeflared\n"), 0600))

	m := NewManager(path)
	require.NoError(t, m.Load())
	assert.Equal(t, "g1", m.GetConfig().KeyName)
}

func TestValidateCrossField(t *testing.T) {
	cfg := DefaultConfig()
	cfg.KeyName = "g1"

	// Default metrics port must not collide with the default grpc endpoint.
	require.NoError(t, cfg.Validate())

	cfg.MetricsPort = 9090 // grpc_endpoint default is localhost:9090
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with grpc_endpoint")

	cfg.MetricsPort = 9100
	cfg.HealthPort = 9100
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be the same")
}

func TestSetFieldValidation(t *testing.T) {
	cfg := DefaultConfig()
	assert.Error(t, cfg.SetField("polling_interval", "not-a-duration"))
	assert.Error(t, cfg.SetField("retry_attempts", "many"))
	assert.Error(t, cfg.SetField("enable_metrics", "yes-please"))
	assert.Error(t, cfg.SetField("log_level", "verbose"))
	assert.Error(t, cfg.SetField("encryption_public_key", "deadbeef"))
}

func TestGetEncryptionPrivateKeyEncrypted(t *testing.T) {
	key := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	t.Run("configured passphrase file", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "private_key")
		passPath := filepath.Join(dir, "elsewhere", "kek")
		require.NoError(t, os.MkdirAll(filepath.Dir(passPath), 0700))
		require.NoError(t, custody.WritePassphraseFile(passPath, "dev-pass"))
		require.NoError(t, custody.SaveEncryptedShareKey(keyPath, key, "dev-pass"))

		cfg := DefaultConfig()
		cfg.EncryptionPrivateKeyPath = keyPath
		cfg.EncryptionKeyPassphrase = passPath

		loaded, err := cfg.GetEncryptionPrivateKey()
		require.NoError(t, err)
		assert.Equal(t, key, loaded)
	})

	t.Run("sibling passphrase fallback", func(t *testing.T) {
		// The container case: the configured passphrase path is stale (it
		// carries the init environment's absolute path) but the file sits
		// beside the env-overridden key path on the same volume.
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "private_key")
		require.NoError(t, custody.WritePassphraseFile(custody.SiblingPassphrasePath(keyPath), "dev-pass"))
		require.NoError(t, custody.SaveEncryptedShareKey(keyPath, key, "dev-pass"))

		cfg := DefaultConfig()
		cfg.EncryptionPrivateKeyPath = keyPath
		cfg.EncryptionKeyPassphrase = "/homes/guardian-01/.timeflare/guardian/encryption_key_passphrase"

		loaded, err := cfg.GetEncryptionPrivateKey()
		require.NoError(t, err)
		assert.Equal(t, key, loaded)
	})

	t.Run("missing passphrase errors", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "private_key")
		require.NoError(t, custody.SaveEncryptedShareKey(keyPath, key, "dev-pass"))

		cfg := DefaultConfig()
		cfg.EncryptionPrivateKeyPath = keyPath

		_, err := cfg.GetEncryptionPrivateKey()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "encrypted")
	})

	t.Run("legacy plaintext still loads", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "private_key")
		require.NoError(t, os.WriteFile(keyPath, key[:], 0600))

		cfg := DefaultConfig()
		cfg.EncryptionPrivateKeyPath = keyPath

		loaded, err := cfg.GetEncryptionPrivateKey()
		require.NoError(t, err)
		assert.Equal(t, key, loaded)
	})
}

func TestWipeEncryptionKeyDropsCacheAndReloads(t *testing.T) {
	key := [32]byte{9, 9, 9}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "private_key")
	require.NoError(t, os.WriteFile(keyPath, key[:], 0600))

	cfg := DefaultConfig()
	cfg.EncryptionPrivateKeyPath = keyPath

	loaded, err := cfg.GetEncryptionPrivateKey()
	require.NoError(t, err)
	assert.Equal(t, key, loaded)

	cfg.WipeEncryptionKey()

	// Reload from file works after a wipe…
	loaded, err = cfg.GetEncryptionPrivateKey()
	require.NoError(t, err)
	assert.Equal(t, key, loaded)

	// …and with the file gone, a wiped cache means no key.
	cfg.WipeEncryptionKey()
	require.NoError(t, os.Remove(keyPath))
	_, err = cfg.GetEncryptionPrivateKey()
	assert.Error(t, err)
}

func TestListAllGroupedMarksDefaults(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, m.Set("log_level", "debug"))

	groups := m.ListAllGrouped()
	item := groups["Monitoring"]["log_level"]
	assert.Equal(t, "debug", item.Value)
	assert.False(t, item.IsDefault)

	assert.True(t, groups["Monitoring"]["log_format"].IsDefault)
}

// Where the key files land is decided by two rules that used to be spelled out
// separately in each getter. A guardian pointed at its own --config-path has to
// keep its keys beside that configuration, and an operator who names a keyring
// directory has to win over that.
func TestKeyPathsFollowTheConfigurationsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "config.yaml"))

	assert.Equal(t, dir, m.GetKeyDirectory(),
		"a configuration outside the default directory should take its keys with it")
	assert.Equal(t, filepath.Join(dir, DefaultPrivateKeyFileName), m.GetPrivateKeyPath())
	assert.Equal(t, filepath.Join(dir, DefaultPublicKeyFileName), m.GetPublicKeyPath())

	// An explicitly named keyring directory outranks the configuration's own.
	elsewhere := t.TempDir()
	require.NoError(t, m.Set("keyring_dir", elsewhere))
	assert.Equal(t, elsewhere, m.GetKeyDirectory())
	assert.Equal(t, filepath.Join(elsewhere, DefaultPrivateKeyFileName), m.GetPrivateKeyPath())

	// As does an explicitly named private key path, over both.
	named := filepath.Join(t.TempDir(), "share.key")
	require.NoError(t, m.Set("encryption_private_key_path", named))
	assert.Equal(t, named, m.GetPrivateKeyPath())
	assert.Equal(t, elsewhere, m.GetKeyDirectory(), "the key directory is unaffected by the key path")
}
