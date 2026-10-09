package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/suxess-it/kubrix-cli/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cmd.NewRoot().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
