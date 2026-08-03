package cli

import (
	"bytes"
	"strings"
	"testing"
)

// The generated configuration file has always advertised
// "flags > env > file > defaults". Until the flag layer existed that was a claim
// about nothing, so this asserts the whole ladder rather than any one rung.
func TestConfigPrecedence(t *testing.T) {
	const (
		defaultLevel = "info" // config.DefaultConfig()
		fileLevel    = "warn"
		envLevel     = "error"
		flagLevel    = "debug"
	)

	for _, c := range []struct {
		name     string
		inFile   bool
		env      string
		flag     string
		expected string
	}{
		{name: "default when nothing says otherwise", expected: defaultLevel},
		{name: "file beats default", inFile: true, expected: fileLevel},
		{name: "env beats file", inFile: true, env: envLevel, expected: envLevel},
		{name: "env beats default", env: envLevel, expected: envLevel},
		{name: "flag beats env and file", inFile: true, env: envLevel, flag: flagLevel, expected: flagLevel},
		{name: "flag beats default", flag: flagLevel, expected: flagLevel},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newFixture(t)
			g.initialised("guardian-one")
			if c.inFile {
				g.mustRun("", "config", "set", "log-level", fileLevel)
			}
			if c.env != "" {
				t.Setenv("GUARDIAN_LOG_LEVEL", c.env)
			}

			args := []string{"start"}
			if c.flag != "" {
				args = append(args, "--log-level", c.flag)
			}
			// start reports the configuration it resolved before it reaches for
			// the chain, so its summary is the effective view. It then fails,
			// because no chain is listening — which is not what this asserts.
			out, _ := g.run("", args...)

			if got := resolvedValue(out, "Log Level:"); got != c.expected {
				t.Errorf("effective log level %q, want %q\n%s", got, c.expected, out)
			}
		})
	}
}

// resolvedValue reads a value out of start's configuration summary.
func resolvedValue(out, label string) string {
	for _, line := range strings.Split(out, "\n") {
		if idx := strings.Index(line, label); idx >= 0 {
			return strings.TrimSpace(line[idx+len(label):])
		}
	}
	return ""
}

// A bad value must be rejected identically however it arrived, because the
// registry is the only thing parsing it.
func TestInvalidValueRejectedFromEveryLayer(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		g := newFixture(t)
		g.initialised("guardian-one")
		_, err := g.run("", "start", "--log-level", "chatty")
		if err == nil || !strings.Contains(err.Error(), "invalid log level") {
			t.Errorf("expected a log-level rejection, got %v", err)
		}
	})
	t.Run("env", func(t *testing.T) {
		g := newFixture(t)
		g.initialised("guardian-one")
		t.Setenv("GUARDIAN_LOG_LEVEL", "chatty")
		_, err := g.run("", "start")
		if err == nil || !strings.Contains(err.Error(), "invalid log level") {
			t.Errorf("expected a log-level rejection, got %v", err)
		}
	})
	t.Run("file", func(t *testing.T) {
		g := newFixture(t)
		g.initialised("guardian-one")
		_, err := g.run("", "config", "set", "log-level", "chatty")
		if err == nil || !strings.Contains(err.Error(), "invalid log level") {
			t.Errorf("expected a log-level rejection, got %v", err)
		}
	})
}

// version must not need a configuration file. It used to fail on a host without
// one, because the initialiser loaded configuration for every command and
// exited the process when that failed.
func TestVersionNeedsNoConfiguration(t *testing.T) {
	g := newFixture(t)
	out, err := g.run("", "version")
	if err != nil {
		t.Fatalf("version failed with no configuration present: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Timeflare guardiand") {
		t.Errorf("version does not name the binary it is:\n%s", out)
	}
}

// A command that cannot run without configuration must fail, not report a
// friendly message and exit zero — a supervisor cannot tell that from success.
func TestCommandsRequiringConfigurationFailWithoutIt(t *testing.T) {
	for _, args := range [][]string{
		{"start"},
		{"register"},
		{"update", "--accepting-secrets=false"},
		{"config", "list"},
		{"config", "doctor"},
		{"key", "backup"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			g := newFixture(t)
			out, err := g.run("", args...)
			if err == nil {
				t.Fatalf("%v succeeded with no configuration:\n%s", args, out)
			}
		})
	}
}

// Colour is decided from the writer, so a redirected or piped run carries no
// escape codes. The daemon's banner goes to a container log.
func TestNoColourOffTerminal(t *testing.T) {
	g := newFixture(t)
	out := g.mustRun("", "version")
	if bytes.Contains([]byte(out), []byte("\x1b[")) {
		t.Errorf("escape codes written to a non-terminal writer:\n%q", out)
	}
}
