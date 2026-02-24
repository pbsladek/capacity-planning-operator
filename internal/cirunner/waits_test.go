package cirunner

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsCRDEstablished(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if isCRDEstablished(crd) {
		t.Fatalf("expected false")
	}
	crd.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{
		{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
	}
	if !isCRDEstablished(crd) {
		t.Fatalf("expected true")
	}
}

func TestDeploymentRolloutStatus(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Spec:       appsv1.DeploymentSpec{},
		Status: appsv1.DeploymentStatus{
			UpdatedReplicas:     1,
			ReadyReplicas:       1,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}
	desired, updated, ready, available, unavailable := deploymentRolloutStatus(dep)
	if desired != 1 || updated != 1 || ready != 1 || available != 1 || unavailable != 0 {
		t.Fatalf("unexpected rollout status: %d %d %d %d %d", desired, updated, ready, available, unavailable)
	}

	rep := int32(3)
	dep.Spec.Replicas = &rep
	desired, _, _, _, _ = deploymentRolloutStatus(dep)
	if desired != 3 {
		t.Fatalf("desired=%d", desired)
	}
}

func TestStatefulSetRolloutReady(t *testing.T) {
	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 3},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 3,
			ReadyReplicas:      1,
			UpdatedReplicas:    0,
			CurrentReplicas:    1,
			CurrentRevision:    "rev-a",
			UpdateRevision:     "rev-a",
		},
	}
	ready, _ := statefulSetRolloutReady(sts)
	if !ready {
		t.Fatalf("expected ready when ReadyReplicas met and revisions match")
	}

	sts.Status.CurrentRevision = "rev-old"
	sts.Status.UpdateRevision = "rev-new"
	ready, _ = statefulSetRolloutReady(sts)
	if ready {
		t.Fatalf("expected not ready when revisions mismatch")
	}

	sts.Status.CurrentRevision = "rev-new"
	sts.Status.UpdateRevision = "rev-new"
	sts.Status.ReadyReplicas = 0
	ready, _ = statefulSetRolloutReady(sts)
	if ready {
		t.Fatalf("expected not ready when ReadyReplicas < desired")
	}
}
