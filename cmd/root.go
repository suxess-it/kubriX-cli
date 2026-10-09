package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/git"
	"github.com/suxess-it/kubrix-cli/internal/state"
	"github.com/suxess-it/kubrix-cli/internal/ui"
)

func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:          "kubrix",
		Short:        "kubriX command line tool",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isInteractive() {
				return cmd.Help()
			}
			showHelp := false
			svc := productionServices()
			err := withUI(cmd, "kubriX", func(ctx context.Context, u ui.UI) error {
				choice, err := menu(u, openCatalog(u))
				if err != nil {
					return err
				}
				switch choice {
				case "demo":
					return runDemo(ctx, u, svc)
				case "install":
					return runInstall(ctx, u, svc)
				case "upgrade":
					return runUpgrade(ctx, u, svc)
				case "delete":
					return runDemoDelete(ctx, u, svc)
				case "help":
					showHelp = true
				}
				return ui.ErrQuit
			})
			if err == nil && showHelp {
				return cmd.Help()
			}
			return err
		},
	}
	root.PersistentFlags().Bool("plain", false, "print line by line instead of using the full-screen interface")
	demo := newDemoCmd()
	demo.AddCommand(newDemoDeleteCmd())
	root.AddCommand(demo, newInstallCmd(), newUpgradeCmd())
	return root
}

// withUI runs flow full-screen in a terminal, and line by line with --plain or without a terminal.
func withUI(cmd *cobra.Command, title string, flow func(context.Context, ui.UI) error) error {
	plain, _ := cmd.Flags().GetBool("plain")
	var err error
	if plain || !isInteractive() {
		err = flow(cmd.Context(), ui.NewPlain(cmd.OutOrStdout()))
	} else {
		logPath := ""
		if dir, dirErr := state.Dir(); dirErr == nil {
			logPath = filepath.Join(dir, "last-run.log")
		}
		err = ui.RunFullscreen(cmd.Context(), title, cmd.OutOrStdout(), logPath, flow)
	}
	if errors.Is(err, ui.ErrQuit) {
		return nil
	}
	return err
}

func isInteractive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		fi, err := f.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func menu(u ui.UI, catalog *state.Catalog) (string, error) {
	description := "Nothing installed yet."
	if catalog.Len() > 0 {
		description = fmt.Sprintf("Saved installations: %d", catalog.Len())
		if last, ok := catalog.Last(); ok {
			description += fmt.Sprintf(" (last: %s/%s on %s)", last.Org, last.Repo, last.Where())
		}
	}
	choice := "demo"
	err := ui.Select(u, "What do you want to do?", description, []ui.Option{
		{Label: "Set up a kind demo platform", Value: "demo"},
		{Label: "Install on an existing cluster", Value: "install"},
		{Label: "Upgrade an installation (opens a pull request)", Value: "upgrade"},
		{Label: "Delete a demo / forget an installation", Value: "delete"},
		{Label: "Show help", Value: "help"},
		{Label: "Quit", Value: "quit"},
	}, &choice)
	if errors.Is(err, ui.ErrAborted) {
		return "", ui.ErrQuit
	}
	return choice, err
}

// githubClient prefers an authenticated gh CLI and falls back to prompting for a PAT.
func githubClient(ctx context.Context, u ui.UI) (*git.GitHub, string, error) {
	token, ghErr := git.GitHubTokenFromCLI()
	if ghErr != nil {
		err := u.Ask(ui.Form{Pages: []ui.Page{{Fields: []ui.Field{ui.Input{
			Key:         "github-token",
			Title:       "GitHub personal access token",
			Description: fmt.Sprintf("%s\n\n(%v)", git.GitHubPATHelp, ghErr),
			Secret:      true,
			Validate:    nonEmpty("token"),
			Value:       &token,
		}}}}})
		if err != nil {
			return nil, "", abortErr(err)
		}
	}
	client, err := git.NewGitHub(token)
	if err != nil {
		return nil, "", err
	}
	var login string
	if err := u.Step(ctx, "Checking GitHub token", func(ctx context.Context) (err error) {
		login, err = client.Validate(ctx)
		return err
	}); err != nil {
		return nil, "", err
	}
	return client, login, nil
}

// abortErr turns a declined question into ui.ErrAborted: call it where the answer was "no" or the question failed,
// never on success. A cancelled form already is a ui.ErrAborted.
func abortErr(err error) error {
	if err == nil {
		return ui.ErrAborted
	}
	return err
}

func nonEmpty(field string) func(string) error {
	return func(s string) error {
		if s == "" {
			return fmt.Errorf("%s must not be empty", field)
		}
		return nil
	}
}
