package gitops

import (
	"context"
	"strings"
	"testing"
)

func TestTokenOnlyInHeaderEnv(t *testing.T) {
	g := New(t.TempDir(), "secret-token", "bot", "bot@example.com")
	var header string
	for _, e := range g.env {
		if strings.HasPrefix(e, "GIT_CONFIG_VALUE_0=") {
			header = e
		}
		if strings.Contains(e, "secret-token") {
			t.Fatalf("raw token must not appear in the environment: %s", e)
		}
	}
	if !strings.Contains(header, "AUTHORIZATION: basic ") {
		t.Fatalf("missing auth header, got %q", header)
	}
}

func TestRunAndLines(t *testing.T) {
	ctx := context.Background()
	g := New(t.TempDir(), "", "bot", "bot@example.com")
	if _, err := g.Run(ctx, "init", "-q", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(ctx, "commit", "-q", "--allow-empty", "-m", "first"); err != nil {
		t.Fatal(err)
	}
	author, err := g.Run(ctx, "log", "-1", "--format=%an <%ae>")
	if err != nil || author != "bot <bot@example.com>" {
		t.Fatalf("author %q, err %v", author, err)
	}
	if lines, err := g.Lines(ctx, "tag", "-l"); err != nil || lines != nil {
		t.Fatalf("expected no tags, got %v %v", lines, err)
	}
	if _, err := g.Run(ctx, "checkout", "does-not-exist"); err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error must carry stderr, got %v", err)
	}
}
