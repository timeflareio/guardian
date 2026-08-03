package cmd

import (
	"runtime"

	"github.com/spf13/cobra"
)

// NewVersionCmd creates the version command
func NewVersionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Long:  `Show version information for the guardian service.`,
		Example: `  # Show version information
  guardiand version`,
		Run: runVersion,
	}

	return cmd
}

func runVersion(cmd *cobra.Command, args []string) {
	printf("Timeflare Guardian Service\n")
	printf("Version:    %s\n", version)
	printf("Go Version: %s\n", runtime.Version())
	printf("OS/Arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	printEmptyLine()
}
