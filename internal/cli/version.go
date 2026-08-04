package cli

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
	u := printer(cmd)
	rev, modified := buildRevision()
	// Name the binary this is, not the project: both binaries carry this command
	// and an operator comparing two versions needs to know which is which.
	u.Printf("Timeflare %s\n", cmd.Root().Name())
	u.Printf("Version:    %s\n", buildVersion())
	if rev != "" {
		state := "clean"
		if modified {
			state = "modified"
		}
		u.Printf("Revision:   %s (%s)\n", rev, state)
	}
	u.Printf("Go Version: %s\n", runtime.Version())
	u.Printf("OS/Arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	u.EmptyLine()
}
