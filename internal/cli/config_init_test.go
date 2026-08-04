package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/timeflareio/guardian/internal/cli/ui"
	"github.com/timeflareio/guardian/internal/custody"
)

// What config init writes is the guardian's whole at-rest footprint, and the
// permission bits are the part no operator checks by hand.
func TestConfigInitWritesExpectedLayoutAndPermissions(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	keyPath := g.get("encryption-private-key-path")
	if keyPath == "" {
		t.Fatal("config init left encryption_private_key_path empty")
	}

	for _, c := range []struct {
		what string
		path string
		mode os.FileMode
	}{
		{"config file", g.configPath, 0600},
		{"share private key", keyPath, 0600},
		{"at-rest passphrase", custody.SiblingPassphrasePath(keyPath), 0600},
		{"keyring passphrase", filepath.Join(g.dir, "keyring_passphrase"), 0600},
		{"public key hex", g.path("public_key.hex"), 0644},
	} {
		if got := g.mode(c.path); got != c.mode {
			t.Errorf("%s (%s): mode %04o, want %04o", c.what, c.path, got, c.mode)
		}
	}

	// The private key must be the encrypted envelope, never a raw 32-byte file:
	// encrypted-at-rest is the default with no plaintext generation path.
	encrypted, err := custody.IsEncryptedKeyFile(keyPath)
	if err != nil {
		t.Fatalf("reading the generated key: %v", err)
	}
	if !encrypted {
		t.Error("generated share key is not encrypted at rest")
	}

	// The configuration must name the passphrase file, not carry the passphrase.
	body, err := os.ReadFile(g.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "at-rest-pass") {
		t.Error("the at-rest passphrase was written into the configuration file")
	}
	if strings.Contains(string(body), "keyring-pass") {
		t.Error("the keyring passphrase was written into the configuration file")
	}

	// The public key on disk, in the configuration, and derived from the private
	// key must all agree — a mismatch here is what 'key backup' later refuses on.
	hexOnDisk, err := os.ReadFile(g.path("public_key.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if configured := g.get("encryption-public-key"); configured != string(hexOnDisk) {
		t.Errorf("configured public key %q does not match public_key.hex %q", configured, hexOnDisk)
	}
}

// Re-running init must not touch an existing configuration: the keys it would
// generate are the ones already in use.
func TestConfigInitRefusesToOverwriteExistingConfig(t *testing.T) {
	g := newFixture(t)
	g.initialised("guardian-one")

	before, err := os.ReadFile(g.configPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := g.get("encryption-private-key-path")
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	out := g.mustRun("", "config", "init", "--key-name", "guardian-two",
		"--keyring-backend", "test", "--keyring-dir", g.dir,
		"--keyring-passphrase", "other", "--auto-generate-key",
		"--encryption-key-passphrase", "other")
	if !strings.Contains(out, "already exists") {
		t.Errorf("expected an already-exists notice, got:\n%s", out)
	}

	after, err := os.ReadFile(g.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("config init rewrote an existing configuration file")
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(keyBefore) != string(keyAfter) {
		t.Error("config init replaced an existing share key — every share encrypted to the old key would be unreadable")
	}
}

// Flag mode is what an automated fleet uses, so a missing flag has to be an
// error rather than a silent fall-through to a prompt that no one will answer.
// init can leave the dashboard reachable rather than withheld, but the password
// it sets has to reach the file as a hash and the plaintext has to be shown, or
// the operator has a dashboard nobody can open.
func TestConfigInitGeneratesADashboardPassword(t *testing.T) {
	g := newFixture(t)
	g.mustRun("", "wallet", "create", "--name", "guardian-one")

	out := g.mustRun("",
		"config", "init",
		"--key-name", "guardian-one",
		"--keyring-backend", "test",
		"--keyring-dir", g.dir,
		"--keyring-passphrase", "keyring-pass",
		"--auto-generate-key",
		"--encryption-key-passphrase", "at-rest-pass",
		"--generate-dashboard-password",
	)

	hash := g.get("dashboard-password-hash")
	if hash == "" {
		t.Fatalf("init set no dashboard password hash:\n%s", out)
	}

	password := printedDashboardPassword(t, out)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		t.Errorf("the stored hash does not verify the password init printed")
	}
	// The plaintext exists only in that one line of output.
	stored, err := os.ReadFile(g.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), password) {
		t.Error("the dashboard password was written to the configuration in plaintext")
	}
}

// The password is never a flag value, so the two ways of supplying one cannot
// both be meant at once.
func TestConfigInitRefusesBothDashboardPasswordSwitches(t *testing.T) {
	g := newFixture(t)
	g.mustRun("", "wallet", "create", "--name", "guardian-one")

	out, err := g.run("",
		"config", "init",
		"--key-name", "guardian-one",
		"--keyring-backend", "test",
		"--keyring-dir", g.dir,
		"--keyring-passphrase", "keyring-pass",
		"--auto-generate-key",
		"--encryption-key-passphrase", "at-rest-pass",
		"--generate-dashboard-password",
		"--dashboard-password-stdin",
	)
	if err == nil {
		t.Fatalf("init accepted both dashboard password switches:\n%s", out)
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("init did not name the conflict: %v", err)
	}
}

// printedDashboardPassword recovers the one line init prints the generated
// password on — the only place it ever exists.
func printedDashboardPassword(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "shown once") || i+1 >= len(lines) {
			continue
		}
		if password := strings.TrimSpace(ui.StripANSI(lines[i+1])); password != "" {
			return password
		}
	}
	t.Fatalf("init did not print the generated dashboard password:\n%s", out)
	return ""
}

func TestConfigInitFlagModeRequiresCompleteFlags(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "auto-generate without an at-rest passphrase",
			args: []string{"--key-name", "g", "--keyring-backend", "test", "--auto-generate-key"},
			want: "--encryption-key-passphrase",
		},
		{
			name: "no key material named at all",
			args: []string{"--key-name", "g", "--keyring-backend", "test", "--keyring-passphrase", "p"},
			want: "--encryption-public-key or --auto-generate-key",
		},
		{
			name: "both key sources named",
			args: []string{"--key-name", "g", "--keyring-backend", "test",
				"--encryption-public-key", strings.Repeat("ab", 32), "--auto-generate-key"},
			want: "cannot use both",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newFixture(t)
			out, err := g.run("", append([]string{"config", "init"}, c.args...)...)
			if err == nil {
				t.Fatalf("expected an error, got success:\n%s", out)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
			if _, statErr := os.Stat(g.configPath); statErr == nil {
				t.Error("a rejected init still wrote a configuration file")
			}
		})
	}
}
