package cli

import (
	"bytes"

	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The command layer had no tests at all until the output layer stopped writing
// to os.Stdout (see internal/cli/ui). These exercise it through the root
// command, the way an operator reaches it, rather than by calling run functions
// directly — so flag parsing, configuration resolution and the printed result
// are all under test.

// guardian is one test's world: a temporary configuration path plus the keyring
// and key directory beside it.
type fixture struct {
	t          *testing.T
	dir        string
	configPath string
	// offhost stands in for storage that is not the guardian's data directory.
	// Backup bundles and their passphrases belong here — both because that is
	// what the storage guidance says, and because CollectKeyringFiles sweeps
	// every file in the data directory into the bundle, so anything left there
	// ends up inside it.
	offhost string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	g := &fixture{
		t:          t,
		dir:        dir,
		configPath: filepath.Join(dir, "config.yaml"),
		offhost:    t.TempDir(),
	}
	// The keyring resolves from the configured directory, and the test backend
	// keeps a passphrase prompt out of the picture.
	t.Setenv("GUARDIAN_KEYRING_BACKEND", "test")
	t.Setenv("GUARDIAN_KEYRING_DIR", dir)
	return g
}

// run executes a command and returns everything it printed. stdin supplies
// answers to any prompt; a command that prompts unexpectedly reads EOF and
// fails rather than hanging.
func (g *fixture) run(stdin string, args ...string) (string, error) {
	g.t.Helper()
	var out bytes.Buffer
	root := rootFor(args)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"--config-path", g.configPath}, args...))
	err := root.Execute()
	return out.String(), err
}

// mustRun fails the test if the command does not succeed.
func (g *fixture) mustRun(stdin string, args ...string) string {
	g.t.Helper()
	out, err := g.run(stdin, args...)
	if err != nil {
		g.t.Fatalf("%v failed: %v\n%s", args, err, out)
	}
	return out
}

// initialised brings the guardian to the state every later command assumes: a
// signing key in the keyring and a configuration file naming it, with a
// generated share key encrypted at rest.
func (g *fixture) initialised(keyName string) {
	g.t.Helper()
	g.mustRun("", "wallet", "create", "--name", keyName)
	g.mustRun("",
		"config", "init",
		"--key-name", keyName,
		"--keyring-backend", "test",
		"--keyring-dir", g.dir,
		"--keyring-passphrase", "keyring-pass",
		"--auto-generate-key",
		"--encryption-key-passphrase", "at-rest-pass",
	)
	// No chain is listening in a unit test, so anything that reaches for one
	// should give up immediately rather than spending the production timeout and
	// its retries. Tests that observe start's resolved configuration only need it
	// to get as far as printing.
	g.mustRun("", "config", "set", "request-timeout", "20ms")
	g.mustRun("", "config", "set", "retry-attempts", "1")
	g.mustRun("", "config", "set", "retry-backoff", "1ms")
}

// get reads one configuration value back through the command layer.
func (g *fixture) get(key string) string {
	g.t.Helper()
	return strings.TrimSpace(g.mustRun("", "config", "get", key))
}

func (g *fixture) path(name string) string { return filepath.Join(g.dir, name) }

// mode returns a file's permission bits, failing if it does not exist.
func (g *fixture) mode(path string) os.FileMode {
	g.t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		g.t.Fatalf("expected %s to exist: %v", path, err)
	}
	return info.Mode().Perm()
}

// daemonVerbs are the commands guardiand owns; everything else belongs to
// guardianctl. The two sets are disjoint, so the first argument decides which
// binary a test is driving — and a verb that moves between them makes the tests
// that use it fail rather than silently testing the wrong root.
var daemonVerbs = map[string]bool{"start": true, "health": true, "version": true}

func rootFor(args []string) *cobra.Command {
	if len(args) > 0 && daemonVerbs[args[0]] {
		return NewGuardiandCmd()
	}
	return NewGuardianctlCmd()
}
