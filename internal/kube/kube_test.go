package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHasDefaultStorageClass(t *testing.T) {
	ctx := context.Background()
	plain := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "slow"}}
	c := &Client{cs: fake.NewClientset(plain)}
	if ok, err := c.HasDefaultStorageClass(ctx); err != nil || ok {
		t.Fatalf("no default expected, got %v %v", ok, err)
	}
	def := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}}}
	c = &Client{cs: fake.NewClientset(plain, def)}
	if ok, err := c.HasDefaultStorageClass(ctx); err != nil || !ok {
		t.Fatalf("default expected, got %v %v", ok, err)
	}
}

func TestLoadBalancerAddress(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "sx-traefik", Namespace: "traefik"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status:     corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{Hostname: "lb.example.net"}}}},
	}
	c := &Client{cs: fake.NewClientset(svc)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addr, err := c.LoadBalancerAddress(ctx, "traefik")
	if err != nil || addr != "lb.example.net" {
		t.Fatalf("got %q %v", addr, err)
	}
	empty := &Client{cs: fake.NewClientset()}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if _, err := empty.LoadBalancerAddress(ctx2, "traefik"); err == nil {
		t.Fatal("expected a timeout without a LoadBalancer")
	}
}
