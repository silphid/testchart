package cmd

import (
	"github.com/silphid/testchart/src/internal"
	"github.com/spf13/cobra"
)

// NewRoot creates the root cobra command
func NewRoot(config *internal.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "testchart",
		Short:        "Helm chart unit testing CLI tool",
		Long:         "Helm chart unit testing CLI tool",
		SilenceUsage: true,
	}
	cmd.PersistentFlags().BoolVarP(&internal.IsVerbose, "verbose", "v", false, "output verbose messages to stderr")
	cmd.PersistentFlags().BoolVar(&config.Debug, "debug", false, "run helm in debug mode and save actual rendered files in test dirs")

	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		return internal.LoadConfig(config)
	}

	return cmd
}
