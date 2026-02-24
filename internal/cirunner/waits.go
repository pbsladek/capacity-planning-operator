package cirunner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

func isCRDEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, cond := range crd.Status.Conditions {
		if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
			return true
		}
	}
	return false
}

func waitForCRDsEstablished(ctx context.Context, c *Clients, names []string, timeout, interval time.Duration) error {
	for _, name := range names {
		desc := fmt.Sprintf("CRD %s established", name)
		err := waitUntil(ctx, timeout, interval, desc, func(ctx context.Context) (bool, error) {
			crd, err := c.APIExtensions.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			return isCRDEstablished(crd), nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func deploymentRolloutStatus(dep *appsv1.Deployment) (desired, updated, ready, available, unavailable int32) {
	desired = 1
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	updated = dep.Status.UpdatedReplicas
	ready = dep.Status.ReadyReplicas
	available = dep.Status.AvailableReplicas
	unavailable = dep.Status.UnavailableReplicas
	return
}

func waitForDeploymentRollout(ctx context.Context, c *Clients, namespace, name string, timeout, interval time.Duration) error {
	return waitUntil(ctx, timeout, interval, fmt.Sprintf("deployment/%s rollout", name), func(ctx context.Context) (bool, error) {
		dep, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		desired, updated, ready, available, unavailable := deploymentRolloutStatus(dep)
		if desired <= 0 {
			return false, nil
		}
		if dep.Status.ObservedGeneration < dep.Generation {
			return false, nil
		}
		return updated >= desired && ready >= desired && available >= desired && unavailable == 0, nil
	})
}

func waitForStatefulSetRollout(ctx context.Context, c *Clients, namespace, name string, timeout, interval time.Duration) error {
	var lastStatus string
	err := waitUntil(ctx, timeout, interval, fmt.Sprintf("statefulset/%s rollout", name), func(ctx context.Context) (bool, error) {
		sts, err := c.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			lastStatus = fmt.Sprintf("get failed: %v", err)
			return false, nil
		}
		ready, status := statefulSetRolloutReady(sts)
		lastStatus = status
		return ready, nil
	})
	if err != nil {
		if strings.TrimSpace(lastStatus) != "" {
			return fmt.Errorf("%w (last status: %s)", err, lastStatus)
		}
		return err
	}
	return nil
}

func waitForKubeSystemBootstrap(ctx context.Context, c *Clients, timeout, interval time.Duration) error {
	return waitUntil(ctx, timeout, interval, "kube-system bootstrap readiness", func(ctx context.Context) (bool, error) {
		nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		if len(nodes.Items) == 0 {
			return false, nil
		}
		readyNodes := 0
		for _, node := range nodes.Items {
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					readyNodes++
					break
				}
			}
		}
		if readyNodes != len(nodes.Items) {
			return false, nil
		}

		coredns, err := c.Clientset.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		desired, updated, ready, available, unavailable := deploymentRolloutStatus(coredns)
		if desired <= 0 || coredns.Status.ObservedGeneration < coredns.Generation {
			return false, nil
		}
		if updated < desired || ready < desired || available < desired || unavailable != 0 {
			return false, nil
		}

		daemonSets, err := c.Clientset.AppsV1().DaemonSets("kube-system").List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		for _, ds := range daemonSets.Items {
			if !daemonSetLooksLikeFlannel(&ds) {
				continue
			}
			if ds.Status.ObservedGeneration < ds.Generation {
				return false, nil
			}
			if ds.Status.DesiredNumberScheduled > 0 && ds.Status.NumberReady < ds.Status.DesiredNumberScheduled {
				return false, nil
			}
		}

		return true, nil
	})
}

func daemonSetLooksLikeFlannel(ds *appsv1.DaemonSet) bool {
	if strings.Contains(ds.Name, "flannel") {
		return true
	}
	for k, v := range ds.Labels {
		if strings.Contains(k, "flannel") || strings.Contains(v, "flannel") {
			return true
		}
	}
	return false
}

func statefulSetRolloutReady(sts *appsv1.StatefulSet) (bool, string) {
	desired := int32(1)
	if sts.Spec.Replicas != nil {
		desired = *sts.Spec.Replicas
	}
	if desired <= 0 {
		return false, "desired replicas <= 0"
	}
	if sts.Status.ObservedGeneration < sts.Generation {
		return false, fmt.Sprintf(
			"observedGeneration=%d generation=%d ready=%d updated=%d",
			sts.Status.ObservedGeneration,
			sts.Generation,
			sts.Status.ReadyReplicas,
			sts.Status.UpdatedReplicas,
		)
	}
	if sts.Status.ReadyReplicas < desired {
		return false, fmt.Sprintf(
			"ready=%d/%d updated=%d currentReplicas=%d currentRevision=%s updateRevision=%s",
			sts.Status.ReadyReplicas,
			desired,
			sts.Status.UpdatedReplicas,
			sts.Status.CurrentReplicas,
			sts.Status.CurrentRevision,
			sts.Status.UpdateRevision,
		)
	}
	if sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
		return true, fmt.Sprintf(
			"ready=%d/%d strategy=OnDelete currentRevision=%s updateRevision=%s",
			sts.Status.ReadyReplicas,
			desired,
			sts.Status.CurrentRevision,
			sts.Status.UpdateRevision,
		)
	}
	if sts.Status.CurrentRevision != "" && sts.Status.UpdateRevision != "" && sts.Status.CurrentRevision != sts.Status.UpdateRevision {
		return false, fmt.Sprintf(
			"revision mismatch current=%s update=%s ready=%d/%d updated=%d",
			sts.Status.CurrentRevision,
			sts.Status.UpdateRevision,
			sts.Status.ReadyReplicas,
			desired,
			sts.Status.UpdatedReplicas,
		)
	}
	if sts.Status.UpdatedReplicas > 0 && sts.Status.UpdatedReplicas < desired {
		return false, fmt.Sprintf(
			"updated=%d/%d ready=%d currentRevision=%s updateRevision=%s",
			sts.Status.UpdatedReplicas,
			desired,
			sts.Status.ReadyReplicas,
			sts.Status.CurrentRevision,
			sts.Status.UpdateRevision,
		)
	}
	return true, fmt.Sprintf(
		"ready=%d/%d updated=%d currentReplicas=%d currentRevision=%s updateRevision=%s",
		sts.Status.ReadyReplicas,
		desired,
		sts.Status.UpdatedReplicas,
		sts.Status.CurrentReplicas,
		sts.Status.CurrentRevision,
		sts.Status.UpdateRevision,
	)
}

func waitForPodsScheduled(ctx context.Context, c *Clients, namespace string, podNames []string, timeout, interval time.Duration) error {
	for _, name := range podNames {
		err := waitUntil(ctx, timeout, interval, fmt.Sprintf("pod/%s scheduled", name), func(ctx context.Context) (bool, error) {
			pod, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			if pod.Spec.NodeName != "" {
				return true, nil
			}
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionTrue {
					return true, nil
				}
			}
			return false, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func waitForPVCsBound(ctx context.Context, c *Clients, namespace string, pvcNames []string, timeout, interval time.Duration) error {
	for _, name := range pvcNames {
		err := waitUntil(ctx, timeout, interval, fmt.Sprintf("pvc/%s bound", name), func(ctx context.Context) (bool, error) {
			pvc, err := c.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			return pvc.Status.Phase == corev1.ClaimBound, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func waitForCapacityPlanReconcile(ctx context.Context, c *Clients, planName string, timeout, interval time.Duration) (*metav1.Time, error) {
	var last *metav1.Time
	err := waitUntil(ctx, timeout, interval, fmt.Sprintf("capacityplan/%s first reconcile", planName), func(ctx context.Context) (bool, error) {
		var cp capacityv1.CapacityPlan
		if err := c.Controller.Get(ctx, types.NamespacedName{Name: planName}, &cp); err != nil {
			return false, nil
		}
		if cp.Status.LastReconcileTime == nil {
			return false, nil
		}
		last = cp.Status.LastReconcileTime.DeepCopy()
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return last, nil
}

func getCapacityPlan(ctx context.Context, c *Clients, planName string) (*capacityv1.CapacityPlan, error) {
	var cp capacityv1.CapacityPlan
	if err := c.Controller.Get(ctx, types.NamespacedName{Name: planName}, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

func getDeploymentByLabel(ctx context.Context, c *Clients, namespace string, selector map[string]string) (*appsv1.Deployment, error) {
	ls := labels.SelectorFromSet(selector).String()
	list, err := c.Clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: ls})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no deployment found in %s with selector %s", namespace, ls)
	}
	dep := list.Items[0]
	return &dep, nil
}

func getFirstPodByLabel(ctx context.Context, c *Clients, namespace string, selector map[string]string) (*corev1.Pod, error) {
	ls := labels.SelectorFromSet(selector).String()
	list, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: ls})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no pod found in %s with selector %s", namespace, ls)
	}
	pod := list.Items[0]
	return &pod, nil
}

func atoiOrZero(s string) int {
	i, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return i
}
