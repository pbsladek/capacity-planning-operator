package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResolvePVCWorkload_DeploymentViaReplicaSet(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"}}
	rsCtrl := true
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-rs",
			Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: "web", Controller: &rsCtrl},
			},
		},
	}
	podCtrl := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc",
			Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: "web-rs", Controller: &podCtrl},
			},
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"},
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(dep, rs, pod).Build()
	r := &CapacityPlanReconciler{Client: c}

	kind, name, ns, err := r.resolvePVCWorkload(context.Background(), "ns", "data")
	if err != nil {
		t.Fatalf("resolve workload: %v", err)
	}
	if kind != "Deployment" || name != "web" || ns != "ns" {
		t.Fatalf("unexpected owner: %s/%s ns=%s", kind, name, ns)
	}
}

func TestResolvePVCWorkload_StatefulSetDirect(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	podCtrl := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-0",
			Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "StatefulSet", Name: "db", Controller: &podCtrl},
			},
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "db-data"},
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	r := &CapacityPlanReconciler{Client: c}

	kind, name, ns, err := r.resolvePVCWorkload(context.Background(), "ns", "db-data")
	if err != nil {
		t.Fatalf("resolve workload: %v", err)
	}
	if kind != "StatefulSet" || name != "db" || ns != "ns" {
		t.Fatalf("unexpected owner: %s/%s ns=%s", kind, name, ns)
	}
}
