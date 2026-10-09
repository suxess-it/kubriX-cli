package platforminstaller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultUpstream is the kubriX source repository; the installer only needs to be told about other ones.
	DefaultUpstream = "suxess-it/kubriX"

	maxWaitSec = 2400
	// install-kubriX-with-job.sh adds the same slack on top of KUBRIX_BOOTSTRAP_MAX_WAIT_TIME.
	slackSec = 600
	logDrain = 10 * time.Second
)

// Kube is the cluster access the platform installer needs; *kube.Client is the production adapter.
type Kube interface {
	EnsureNamespace(ctx context.Context, name string) error
	ApplySecret(ctx context.Context, ns, name string, values map[string]string) error
	DeleteJob(ctx context.Context, ns, name string) error
	ApplyManifests(ctx context.Context, manifests []byte) error
	WaitForJobPod(ctx context.Context, ns, job string) (string, error)
	StreamLogs(ctx context.Context, ns, pod string, w io.Writer) error
	WaitForJob(ctx context.Context, ns, name string) (bool, error)
	JobDiagnostics(ctx context.Context, ns, name string) string
}

// Progress reports what the run is doing; ui.UI satisfies it.
type Progress interface {
	Step(ctx context.Context, title string, fn func(context.Context) error) error
	// Begin marks a long phase without a spinner (e.g. while logs stream); call end when it finishes.
	Begin(title string) (end func(error))
	Logs() io.Writer
	Info(msg string)
	OK(msg string)
}

// Spec is what the platform installer is told about the platform it installs.
type Spec struct {
	RepoURL        string
	RepoToken      string
	GitUserName    string
	Domain         string
	DNSProvider    string
	CloudProvider  string
	TargetType     string
	ClusterType    string // kind | k8s
	UpstreamRepo   string
	UpstreamBranch string
	// Bootstrap makes the installer bootstrap the (empty) platform repository itself.
	Bootstrap bool
	// ExcludedApps are applications left out while bootstrapping; only meaningful for a fresh repository.
	ExcludedApps []string
}

// SecretValues are the keys of the installer's Secret, the env contract with install-platform.sh.
func (s Spec) SecretValues() map[string]string {
	v := map[string]string{
		"KUBRIX_REPO":            s.RepoURL,
		"KUBRIX_REPO_PASSWORD":   s.RepoToken,
		"KUBRIX_GIT_USER_NAME":   s.GitUserName,
		"KUBRIX_DOMAIN":          s.Domain,
		"KUBRIX_DNS_PROVIDER":    s.DNSProvider,
		"KUBRIX_TARGET_TYPE":     s.TargetType,
		"KUBRIX_CLUSTER_TYPE":    s.ClusterType,
		"KUBRIX_BOOTSTRAP":       strconv.FormatBool(s.Bootstrap),
		"KUBRIX_INSTALLER":       "true",
		"KUBRIX_UPSTREAM_BRANCH": s.UpstreamBranch,
	}
	if s.CloudProvider != "" {
		v["KUBRIX_CLOUD_PROVIDER"] = s.CloudProvider
	}
	if len(s.ExcludedApps) > 0 {
		v["KUBRIX_APP_EXCLUDE"] = strings.Join(s.ExcludedApps, " ")
	}
	if s.UpstreamRepo != DefaultUpstream {
		v["KUBRIX_UPSTREAM_REPO"] = "https://github.com/" + s.UpstreamRepo
	}
	return v
}

// Options configure one Run.
type Options struct {
	Spec     Spec
	Manifest []byte
	// ReplaceJob removes the Job of a previous run first; a finished Job cannot be updated in place.
	ReplaceJob bool
}

// Run applies the installer's Secret and manifests, follows its logs and waits until the Job ends.
func Run(ctx context.Context, kc Kube, p Progress, o Options) error {
	if err := p.Step(ctx, "Starting the installer", func(ctx context.Context) error {
		if err := kc.EnsureNamespace(ctx, Namespace); err != nil {
			return err
		}
		if err := kc.ApplySecret(ctx, Namespace, SecretName, o.Spec.SecretValues()); err != nil {
			return err
		}
		if o.ReplaceJob {
			if err := kc.DeleteJob(ctx, Namespace, JobName); err != nil {
				return err
			}
		}
		return kc.ApplyManifests(ctx, o.Manifest)
	}); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, (maxWaitSec+slackSec)*time.Second)
	defer cancel()

	var pod string
	if err := p.Step(ctx, "Waiting for the installer pod", func(ctx context.Context) (err error) {
		pod, err = kc.WaitForJobPod(ctx, Namespace, JobName)
		return err
	}); err != nil {
		return err
	}

	end := p.Begin("Installing kubriX (installer logs)")
	logsDone := make(chan error, 1)
	logCtx, stopLogs := context.WithCancel(ctx)
	defer stopLogs()
	go func() { logsDone <- kc.StreamLogs(logCtx, Namespace, pod, p.Logs()) }()

	succeeded, err := kc.WaitForJob(ctx, Namespace, JobName)
	select {
	case <-logsDone:
	case <-time.After(logDrain):
		stopLogs()
	}
	if err == nil && !succeeded {
		p.Info(kc.JobDiagnostics(context.WithoutCancel(ctx), Namespace, JobName))
		err = errors.New("the kubriX install job failed; see the installer logs")
	} else if err != nil {
		err = fmt.Errorf("waiting for the install job: %w (the cluster keeps running; rerun the install or delete it)", err)
	}
	end(err)
	if err != nil {
		return err
	}
	p.OK("kubriX installed")
	return nil
}
