// Package gitops runs the git binary; go-git cannot do the three-way merges upgrades need.
package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func Available() error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is not installed; it is needed to bootstrap from a release and to upgrade")
	}
	return nil
}

type Git struct {
	Dir string
	env []string
}

// New binds git to dir. The token is passed as a GitHub auth header through GIT_CONFIG_* variables,
// so it never appears in arguments, remote URLs or .git/config.
func New(dir, token, userName, userEmail string) *Git {
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME="+userName, "GIT_AUTHOR_EMAIL="+userEmail,
		"GIT_COMMITTER_NAME="+userName, "GIT_COMMITTER_EMAIL="+userEmail,
	)
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
			"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+basic,
		)
	}
	return &Git{Dir: dir, env: env}
}

// Run executes git in g.Dir and returns trimmed stdout; errors carry stderr.
func (g *Git) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	cmd.Env = g.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Lines runs git and splits stdout into non-empty lines.
func (g *Git) Lines(ctx context.Context, args ...string) ([]string, error) {
	out, err := g.Run(ctx, args...)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

func GitHubURL(slug string) string { return "https://github.com/" + slug + ".git" }
