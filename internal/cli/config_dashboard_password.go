package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/timeflareio/guardian/internal/config"
	"github.com/timeflareio/guardian/internal/dashboard"
)

// generatedPasswordBytes is the entropy behind --generate. At 32 bytes, online
// guessing against a bcrypt verification is not a threat model, which is what
// makes a leaked config file's offline attack worthless too.
const generatedPasswordBytes = 32

// NewConfigSetDashboardPasswordCmd creates the config set-dashboard-password command.
//
// The password is never taken as an argument: arguments land in shell history
// and in `ps`. What the config file stores is the bcrypt hash, so the plaintext
// exists only for as long as it takes to hash it.
func NewConfigSetDashboardPasswordCmd() *cobra.Command {
	var generate bool
	var fromStdin bool

	cmd := &cobra.Command{
		Use:   "set-dashboard-password",
		Short: "Set the operator dashboard password",
		Long: `Set the password for the operator dashboard, stored as a bcrypt hash in
dashboard_password_hash.

The dashboard authenticates with HTTP Basic auth as user "` + dashboard.Username + `". Beyond
loopback the dashboard is not served at all until this is set.

Prompts twice with no echo by default. The password is never accepted as a
command-line argument, because arguments land in shell history and in ps.`,
		Example: `  # Prompt for a password (twice, no echo)
  guardiand config set-dashboard-password

  # Generate a strong one and print it once
  guardiand config set-dashboard-password --generate

  # Provision a chosen password from a script or an image build
  printf '%s' "$DASHBOARD_PASSWORD" | guardiand config set-dashboard-password --stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSetDashboardPassword(generate, fromStdin)
		},
	}

	cmd.Flags().BoolVar(&generate, "generate", false, "Generate a strong password, print it once, and store only its hash")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "Read the password from stdin (for scripts and image builds)")

	return cmd
}

func runConfigSetDashboardPassword(generate, fromStdin bool) error {
	if generate && fromStdin {
		return errors.New("--generate and --stdin are mutually exclusive")
	}

	if err := cfgManager.Load(); err != nil {
		return err
	}

	var password string
	var err error
	switch {
	case generate:
		password, err = generatePassword()
	case fromStdin:
		password, err = readPasswordFromStdin()
	default:
		password, err = promptForDashboardPassword()
	}
	if err != nil {
		return err
	}

	hash, err := config.HashPassword(password)
	if err != nil {
		return err
	}
	if err := cfgManager.Set("dashboard_password_hash", hash); err != nil {
		return errors.Wrap(err, "failed to set dashboard_password_hash")
	}
	if err := cfgManager.Save(); err != nil {
		return errors.Wrap(err, "failed to save config")
	}

	if generate {
		// stdout, once, and never the log: this is the only moment the password
		// exists anywhere. Printed before the advisories so it cannot scroll off
		// behind them.
		printEmptyLine()
		printNote("Dashboard password (shown once — store it now):")
		printf("%s\n", password)
	}

	printEmptyLine()
	printSuccess("Dashboard password set (stored as a bcrypt hash)")
	printNote("Sign in as user %q.", dashboard.Username)

	effective := cfgManager.GetConfig()
	if effective.DashboardAuthRequired() && !effective.DashboardTLSEnabled() {
		printNote("Not encrypted: Basic auth defends against unauthorised readers, not against a network eavesdropper.")
		printNote("Set dashboard_tls_cert_file and dashboard_tls_key_file, or front the port with a TLS proxy.")
	}
	printEmptyLine()
	return nil
}

// reportDashboardExposure states what the dashboard will do at startup, and
// names the command that fixes the one state where it will not be served.
//
// It never fails `config doctor`: a withheld dashboard is a page an operator
// cannot open, not a guardian that cannot reveal, and doctor's exit status
// answers the second question.
func reportDashboardExposure(cfg *config.Config) {
	switch {
	case !cfg.EnableDashboard:
		printNote("Dashboard: disabled (enable_dashboard is false)")
	case cfg.DashboardWithheld():
		printWarning("Dashboard: NOT served — %s:%d is beyond loopback and no password is set",
			cfg.BindAddress, cfg.DashboardPort)
		printNote("Fix: guardiand config set-dashboard-password")
		printNote("The guardian is otherwise unaffected: health, metrics and reveals continue.")
	case !cfg.DashboardAuthRequired():
		printSuccess("Dashboard: served on loopback %s:%d without a credential",
			cfg.BindAddress, cfg.DashboardPort)
	case cfg.DashboardTLSEnabled():
		printSuccess("Dashboard: authenticated over TLS on %s:%d (user %q)",
			cfg.BindAddress, cfg.DashboardPort, dashboard.Username)
	default:
		printSuccess("Dashboard: authenticated on %s:%d (user %q)",
			cfg.BindAddress, cfg.DashboardPort, dashboard.Username)
		printNote("Not encrypted: set dashboard_tls_cert_file and dashboard_tls_key_file, or front it with a TLS proxy.")
	}
}

// generatePassword returns a random password, URL-safe so it survives being
// pasted into a browser prompt or a curl -u argument unmangled.
func generatePassword() (string, error) {
	raw := make([]byte, generatedPasswordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.Wrap(err, "failed to generate password")
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// readPasswordFromStdin takes the first line of stdin, so a script can pipe a
// chosen password in without it ever reaching argv.
func readPasswordFromStdin() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	// A pipe that ends without a newline is the common shape (printf '%s'), so
	// EOF with content read is success, not failure.
	if err != nil && !(err == io.EOF && line != "") {
		return "", errors.Wrap(err, "failed to read password from stdin")
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("no password on stdin")
	}
	return password, nil
}

// promptForDashboardPassword reads the password twice with no echo. The
// confirmation is the point: a mistyped password that is only discovered later
// means a dashboard nobody can open.
func promptForDashboardPassword() (string, error) {
	printText("Dashboard password: ")
	first, err := readPasswordInput()
	if err != nil {
		return "", errors.Wrap(err, "failed to read password")
	}
	printText("Confirm password: ")
	second, err := readPasswordInput()
	if err != nil {
		return "", errors.Wrap(err, "failed to read password")
	}

	password := strings.TrimRight(string(first), "\r\n")
	if password != strings.TrimRight(string(second), "\r\n") {
		return "", errors.New("passwords do not match")
	}
	if password == "" {
		return "", errors.New("password cannot be empty")
	}
	return password, nil
}
