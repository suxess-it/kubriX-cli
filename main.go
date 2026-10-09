package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/suxess-it/kubrix-cli/cmd"
)

// Set by the release build through linker flags (-X main.version=...); see .goreleaser.yaml and container/Dockerfile.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	bi, ok := debug.ReadBuildInfo()
	build := cmd.BuildInfo{Version: version, Commit: commit, Date: date}.WithRuntime(bi, ok)
	if err := cmd.NewRoot(build, cmd.FeaturesFromEnv(os.Getenv)).ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
