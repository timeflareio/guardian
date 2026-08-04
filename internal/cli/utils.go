package cli

import (
	"github.com/timeflareio/guardian/internal/chain"
	"github.com/timeflareio/guardian/internal/config"
)

// getGuardianAddress resolves the guardian address from the keyring
// in-process — no subprocess, no passphrase piping.
func getGuardianAddress(cfg *config.Config) (string, error) {
	return chain.ResolveKeyAddress(cfg, cfg.KeyName)
}
