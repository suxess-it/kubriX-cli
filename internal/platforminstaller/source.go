package platforminstaller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/suxess-it/kubrix-cli/internal/gitops"
	"github.com/suxess-it/kubrix-cli/internal/platformrepo"
	"github.com/suxess-it/kubrix-cli/internal/version"
)

const defaultRawURL = "https://raw.githubusercontent.com"

// Source reads the installer's inputs from the kubriX source repository on GitHub and the installer image registry.
// The zero value reads from GitHub and ghcr.io.
type Source struct {
	// Token is only used as a fallback for private forks.
	Token string
	// RawURL is where raw file contents are served; raw.githubusercontent.com when empty.
	RawURL string
	// Image is the installer image repository; ImageRepo when empty.
	Image string
	// InsecureRegistry allows a registry over plain HTTP, for a local one.
	InsecureRegistry bool
}

func (s Source) rawURL(repo, ref, path string) string {
	base := strings.TrimSuffix(s.RawURL, "/")
	if base == "" {
		base = defaultRawURL
	}
	return fmt.Sprintf("%s/%s/%s/%s", base, repo, refPath(ref), path)
}

// refPath resolves a kubriX source ref: a release tag (vX.Y.Z) or a branch.
func refPath(ref string) string {
	if version.IsRelease(ref) {
		return "refs/tags/" + ref
	}
	return "refs/heads/" + ref
}

// Manifest downloads install-manifests.yaml of the ref.
func (s Source) Manifest(ctx context.Context, repo, ref string) ([]byte, error) {
	data, status, err := s.fetch(ctx, repo, ref, "install-manifests.yaml")
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("fetching %s: HTTP %d (is the branch pushed?)", s.rawURL(repo, ref, "install-manifests.yaml"), status)
	}
	return data, err
}

// Apps lists the applications the target type installs on the cluster type, rendering the target's template
// the way the bootstrap does.
func (s Source) Apps(ctx context.Context, repo, ref, targetType, clusterType string) ([]string, error) {
	values := platformrepo.TargetValuesPath(targetType)
	for _, path := range []string{platformrepo.TemplatePath(values), values} {
		data, status, err := s.fetch(ctx, repo, ref, path)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			continue
		}
		if platformrepo.IsTemplate(path) {
			return platformrepo.TemplateApps(path, data, platformrepo.ConfigInput{ClusterType: clusterType, DNSProvider: "none"})
		}
		return platformrepo.Apps(data)
	}
	return nil, fmt.Errorf("no values file for target type %q on %s@%s", targetType, repo, ref)
}

// fetch tries anonymously first: raw.githubusercontent.com answers 404 for public repos
// when sent a token that cannot see them (e.g. a fine-grained PAT scoped to another org).
// The token is only used as a fallback for private forks.
func (s Source) fetch(ctx context.Context, repo, ref, path string) ([]byte, int, error) {
	url := s.rawURL(repo, ref, path)
	data, status, err := get(ctx, url, "")
	if status == http.StatusNotFound && s.Token != "" {
		data, status, err = get(ctx, url, s.Token)
	}
	return data, status, err
}

func get(ctx context.Context, url, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// ImageTagExists reports whether the installer image has the tag.
func (s Source) ImageTagExists(ctx context.Context, tag string) (bool, error) {
	repo := s.Image
	if repo == "" {
		repo = ImageRepo
	}
	var opts []name.Option
	if s.InsecureRegistry {
		opts = append(opts, name.Insecure)
	}
	ref, err := name.NewTag(repo+":"+tag, opts...)
	if err != nil {
		return false, err
	}
	_, err = remote.Head(ref, remote.WithContext(ctx))
	var terr *transport.Error
	if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

// GitURL is the clone URL of the source repository.
func (s Source) GitURL(repo string) string { return gitops.GitHubURL(repo) }
