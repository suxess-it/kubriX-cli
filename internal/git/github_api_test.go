package git

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	gh "github.com/google/go-github/v92/github"
)

// newTestGitHub returns a GitHub adapter that talks to handler instead of api.github.com.
func newTestGitHub(t *testing.T, handler http.Handler) *GitHub {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	c, err := gh.NewClient(gh.WithAuthToken("tok3n"), gh.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	return &GitHub{c: c, token: "tok3n"}
}

func TestGitHubTokenAndCloneURL(t *testing.T) {
	g, err := NewGitHub("tok3n")
	if err != nil {
		t.Fatal(err)
	}
	if g.Token() != "tok3n" {
		t.Errorf("token %q", g.Token())
	}
	if got := g.CloneURL("acme", "demo"); got != "https://github.com/acme/demo.git" {
		t.Errorf("clone URL %q", got)
	}
}

func TestGitHubSendsTheToken(t *testing.T) {
	var auth string
	g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	if _, err := g.Orgs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok3n" {
		t.Errorf("Authorization %q", auth)
	}
}

func TestGitHubRepoState(t *testing.T) {
	for name, tc := range map[string]struct {
		repoStatus, commitsStatus int
		want                      RepoState
		wantErr                   bool
	}{
		"missing":   {http.StatusNotFound, 0, RepoMissing, false},
		"empty":     {http.StatusOK, http.StatusConflict, RepoEmpty, false},
		"non-empty": {http.StatusOK, http.StatusOK, RepoNonEmpty, false},
		"forbidden": {http.StatusForbidden, 0, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status, body := tc.repoStatus, `{"name":"demo"}`
				if strings.HasSuffix(r.URL.Path, "/commits") {
					status, body = tc.commitsStatus, `[{"sha":"abc"}]`
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(body))
				} else {
					_, _ = w.Write([]byte(`{"message":"nope"}`))
				}
			}))
			got, err := g.RepoState(context.Background(), "acme", "demo")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("got %v, %v; want %v (error %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestGitHubOrgsFollowsPagesAndSorts(t *testing.T) {
	g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"login":"acme"}]`))
			return
		}
		w.Header().Set("Link", `<`+"http://"+r.Host+`/user/orgs?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"login":"zeta"},{"login":"beta"}]`))
	}))
	orgs, err := g.Orgs(context.Background())
	if err != nil || !slices.Equal(orgs, []string{"acme", "beta", "zeta"}) {
		t.Errorf("orgs %v, %v", orgs, err)
	}
}

func TestGitHubCreatePrivateRepo(t *testing.T) {
	var got map[string]any
	var path string
	g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	if err := g.CreatePrivateRepo(context.Background(), "acme", "demo"); err != nil {
		t.Fatal(err)
	}
	if path != "POST /orgs/acme/repos" || got["name"] != "demo" || got["private"] != true {
		t.Errorf("%s %v", path, got)
	}
}

func TestGitHubDeleteRepoExplainsMissingScope(t *testing.T) {
	g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Must have admin rights"}`))
	}))
	err := g.DeleteRepo(context.Background(), "acme", "demo")
	if err == nil || !strings.Contains(err.Error(), "delete_repo") {
		t.Errorf("got %v", err)
	}
}

func TestGitHubValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		scopes  *string
		want    string
		wantErr string
	}{
		"fine-grained token":   {nil, "octocat", ""},
		"classic with scopes":  {ptr("repo, read:org, workflow"), "octocat", ""},
		"classic missing some": {ptr("repo"), "", "read:org, workflow"},
	} {
		t.Run(name, func(t *testing.T) {
			g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.scopes != nil {
					w.Header().Set("X-OAuth-Scopes", *tc.scopes)
				}
				_, _ = w.Write([]byte(`{"login":"octocat"}`))
			}))
			got, err := g.Validate(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("got %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got %q, %v", got, err)
			}
		})
	}
}

func TestGitHubRejectsABadToken(t *testing.T) {
	g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	if _, err := g.Validate(context.Background()); err == nil || !strings.Contains(err.Error(), "token rejected") {
		t.Errorf("got %v", err)
	}
}

func TestGitHubReleaseTagsKeepsOnlyReleasesNewestFirst(t *testing.T) {
	g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"v7.0.0"},{"name":"nightly"},{"name":"v8.0.0"},{"name":"v7.1.0"}]`))
	}))
	tags, err := g.ReleaseTags(context.Background(), "suxess-it", "kubriX")
	if err != nil || !slices.Equal(tags, []string{"v8.0.0", "v7.1.0", "v7.0.0"}) {
		t.Errorf("tags %v, %v", tags, err)
	}
}

func TestGitHubOpenPullRequest(t *testing.T) {
	t.Run("creates one", func(t *testing.T) {
		var created map[string]any
		g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				if r.URL.Query().Get("head") != "acme:kubrix-upgrade-v8.0.0" || r.URL.Query().Get("base") != "main" {
					t.Errorf("query %v", r.URL.Query())
				}
				_, _ = w.Write([]byte(`[]`))
			case http.MethodPost:
				_ = json.NewDecoder(r.Body).Decode(&created)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"html_url":"https://github.com/acme/demo/pull/7"}`))
			}
		}))
		url, err := g.OpenPullRequest(context.Background(), "acme", "demo", "kubrix-upgrade-v8.0.0", "main", "title", "body")
		if err != nil || url != "https://github.com/acme/demo/pull/7" {
			t.Fatalf("%q %v", url, err)
		}
		if created["title"] != "title" || created["head"] != "kubrix-upgrade-v8.0.0" || created["body"] != "body" {
			t.Errorf("created %v", created)
		}
	})
	t.Run("updates the open one", func(t *testing.T) {
		var edited map[string]any
		var methods []string
		g := newTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			methods = append(methods, r.Method)
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`[{"number":3}]`))
			case http.MethodPatch:
				_ = json.NewDecoder(r.Body).Decode(&edited)
				_, _ = w.Write([]byte(`{"html_url":"https://github.com/acme/demo/pull/3"}`))
			}
		}))
		url, err := g.OpenPullRequest(context.Background(), "acme", "demo", "kubrix-upgrade-v8.0.0", "main", "new title", "new body")
		if err != nil || url != "https://github.com/acme/demo/pull/3" {
			t.Fatalf("%q %v", url, err)
		}
		if !slices.Equal(methods, []string{"GET", "PATCH"}) || edited["title"] != "new title" || edited["body"] != "new body" {
			t.Errorf("methods %v edited %v", methods, edited)
		}
	})
}

func ptr[T any](v T) *T { return &v }
