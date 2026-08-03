package cmd

import (
	"bufio"
	"os"
	"strings"

	"github.com/timeflareio/guardian/blockchain"
	"github.com/timeflareio/guardian/config"
)

// promptForConfirmation asks the user for confirmation
func promptForConfirmation(message string) bool {
	printf("%s [y/N]: ", message)

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// getGuardianAddress resolves the guardian address from the keyring
// in-process — no subprocess, no passphrase piping.
func getGuardianAddress(cfg *config.Config) (string, error) {
	return blockchain.ResolveKeyAddress(cfg, cfg.KeyName)
}
