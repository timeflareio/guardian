package main

import (
	"fmt"
	"os"

	"github.com/timeflareio/guardian/internal/cli"
)

func main() {
	rootCmd := cli.NewGuardianctlCmd()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(rootCmd.OutOrStderr(), err)
		os.Exit(1)
	}
}
