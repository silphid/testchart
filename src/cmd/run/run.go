package run

import (
	"context"

	"github.com/silphid/testchart/src/internal"
	"github.com/spf13/cobra"
)

// New creates a cobra command
func New(config *internal.Config) *cobra.Command {

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Runs chart tests",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), config)
		},
	}

	return cmd
}

func run(ctx context.Context, config *internal.Config) error {
	return internal.Run(config)
}
