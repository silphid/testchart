package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/silphid/testchart/src/cmd"
	"github.com/silphid/testchart/src/cmd/run"
	"github.com/silphid/testchart/src/cmd/versioning"
	"github.com/silphid/testchart/src/internal"
)

var version string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		<-ctx.Done()
		stop()
	}()

	var config internal.Config
	rootCmd := cmd.NewRoot(&config)
	rootCmd.AddCommand(run.New(&config))
	rootCmd.AddCommand(versioning.New(version))

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(-1)
	}
}
