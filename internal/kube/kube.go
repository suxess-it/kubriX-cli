package kube

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

const fieldManager = "kubrix-cli"

var applyOpts = metav1.ApplyOptions{FieldManager: fieldManager, Force: true}

type Client struct {
	cs     kubernetes.Interface
	dyn    dynamic.Interface
	mapper meta.RESTMapper
}

func New(cfg *rest.Config) (*Client, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(cs.Discovery()))
	return &Client{cs: cs, dyn: dyn, mapper: mapper}, nil
}

func (c *Client) EnsureNamespace(ctx context.Context, name string) error {
	_, err := c.cs.CoreV1().Namespaces().Apply(ctx, corev1ac.Namespace(name), applyOpts)
	return err
}

// ApplySecret uses server-side apply on .data so keys dropped between runs are removed.
func (c *Client) ApplySecret(ctx context.Context, ns, name string, values map[string]string) error {
	data := make(map[string][]byte, len(values))
	for k, v := range values {
		data[k] = []byte(v)
	}
	return c.ApplySecretData(ctx, ns, name, data)
}

func (c *Client) ApplySecretData(ctx context.Context, ns, name string, data map[string][]byte) error {
	_, err := c.cs.CoreV1().Secrets(ns).Apply(ctx, corev1ac.Secret(name, ns).WithType(corev1.SecretTypeOpaque).WithData(data), applyOpts)
	return err
}

// HasDefaultStorageClass reports whether a StorageClass is marked as the cluster default.
func (c *Client) HasDefaultStorageClass(ctx context.Context) (bool, error) {
	list, err := c.cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	for _, sc := range list.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" ||
			sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
			return true, nil
		}
	}
	return false, nil
}

// IsClusterAdmin checks the rights the installer Job needs (it binds cluster-admin to its ServiceAccount).
func (c *Client) IsClusterAdmin(ctx context.Context) (bool, error) {
	review, err := c.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: "*", Group: "*", Resource: "*"},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}

// LoadBalancerAddress waits until a LoadBalancer Service in ns has an external IP or hostname.
func (c *Client) LoadBalancerAddress(ctx context.Context, ns string) (string, error) {
	var addr string
	err := poll(ctx, 5*time.Second, func() (bool, error) {
		list, err := c.cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		for _, svc := range list.Items {
			if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
				continue
			}
			for _, ing := range svc.Status.LoadBalancer.Ingress {
				if addr = cmp.Or(ing.IP, ing.Hostname); addr != "" {
					return true, nil
				}
			}
		}
		return false, nil
	})
	return addr, err
}

func (c *Client) SecretValue(ctx context.Context, ns, name, key string) (string, error) {
	s, err := c.cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	v, ok := s.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s has no key %q", ns, name, key)
	}
	return string(v), nil
}

// DeleteJob removes a previous install Job (Jobs are immutable and a finished one never re-runs).
func (c *Client) DeleteJob(ctx context.Context, ns, name string) error {
	policy := metav1.DeletePropagationForeground
	err := c.cs.BatchV1().Jobs(ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return poll(ctx, 2*time.Second, func() (bool, error) {
		_, err := c.cs.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

// ApplyManifests server-side applies every document of a multi-document YAML stream.
func (c *Client) ApplyManifests(ctx context.Context, manifests []byte) error {
	dec := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(manifests), 4096)
	for {
		obj := &unstructured.Unstructured{}
		if err := dec.Decode(&obj.Object); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if len(obj.Object) == 0 {
			continue
		}
		gvk := obj.GroupVersionKind()
		mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return fmt.Errorf("%s %s: %w", gvk.Kind, obj.GetName(), err)
		}
		var ri dynamic.ResourceInterface = c.dyn.Resource(mapping.Resource)
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			ns := obj.GetNamespace()
			if ns == "" {
				ns = metav1.NamespaceDefault
			}
			ri = c.dyn.Resource(mapping.Resource).Namespace(ns)
		}
		if _, err := ri.Apply(ctx, obj.GetName(), obj, applyOpts); err != nil {
			return fmt.Errorf("applying %s %s: %w", gvk.Kind, obj.GetName(), err)
		}
	}
}

var imageErrors = []string{"ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError"}

// WaitForJobPod returns the Job's pod once it has left Pending, failing fast on image/config errors.
func (c *Client) WaitForJobPod(ctx context.Context, ns, job string) (string, error) {
	var name string
	err := poll(ctx, 2*time.Second, func() (bool, error) {
		pod, err := c.jobPod(ctx, ns, job)
		if err != nil || pod == nil {
			return false, err
		}
		if pod.Status.Phase != corev1.PodPending {
			name = pod.Name
			return true, nil
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil && slices.Contains(imageErrors, w.Reason) {
				return false, fmt.Errorf("installer pod %s: %s: %s", pod.Name, w.Reason, w.Message)
			}
		}
		return false, nil
	})
	return name, err
}

// StreamLogs follows the pod's logs until the pod terminates, reconnecting if the stream drops.
func (c *Client) StreamLogs(ctx context.Context, ns, pod string, w io.Writer) error {
	opts := &corev1.PodLogOptions{Follow: true}
	for {
		stream, err := c.cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, stream)
		_ = stream.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p, err := c.cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			return nil
		}
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			_, _ = fmt.Fprintf(w, "log stream interrupted (%v), reconnecting...\n", copyErr)
		}
		opts.SinceTime = &metav1.Time{Time: time.Now()}
	}
}

// WaitForJob blocks until the Job reports Complete (true) or Failed (false).
func (c *Client) WaitForJob(ctx context.Context, ns, name string) (bool, error) {
	var succeeded bool
	err := poll(ctx, 2*time.Second, func() (bool, error) {
		job, err := c.cs.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, cond := range job.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}
			switch cond.Type {
			case "Complete":
				succeeded = true
				return true, nil
			case "Failed":
				return true, nil
			}
		}
		return false, nil
	})
	return succeeded, err
}

func (c *Client) JobDiagnostics(ctx context.Context, ns, name string) string {
	var b strings.Builder
	job, err := c.cs.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Sprintf("could not read job %s/%s: %v", ns, name, err)
	}
	fmt.Fprintf(&b, "Job %s/%s: succeeded=%d failed=%d\n", ns, name, job.Status.Succeeded, job.Status.Failed)
	for _, cond := range job.Status.Conditions {
		fmt.Fprintf(&b, "  condition %s=%s: %s %s\n", cond.Type, cond.Status, cond.Reason, cond.Message)
	}
	if pod, err := c.jobPod(ctx, ns, name); err == nil && pod != nil {
		fmt.Fprintf(&b, "Pod %s: phase=%s\n", pod.Name, pod.Status.Phase)
		for _, cs := range pod.Status.ContainerStatuses {
			if t := cs.State.Terminated; t != nil {
				fmt.Fprintf(&b, "  container %s terminated: exitCode=%d reason=%s\n", cs.Name, t.ExitCode, t.Reason)
			}
		}
	}
	return b.String()
}

func (c *Client) IngressHosts(ctx context.Context) ([]string, error) {
	list, err := c.cs.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, ing := range list.Items {
		for _, rule := range ing.Spec.Rules {
			if rule.Host != "" && !slices.Contains(hosts, rule.Host) {
				hosts = append(hosts, rule.Host)
			}
		}
	}
	slices.Sort(hosts)
	return hosts, nil
}

func (c *Client) jobPod(ctx context.Context, ns, job string) (*corev1.Pod, error) {
	pods, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + job})
	if err != nil || len(pods.Items) == 0 {
		return nil, err
	}
	return &pods.Items[0], nil
}

func poll(ctx context.Context, interval time.Duration, done func() (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		ok, err := done()
		if err != nil || ok {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
