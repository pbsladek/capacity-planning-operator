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
	return waitUntil(ctx, timeout, interval, fmt.Sprintf("statefulset/%s rollout", name), func(ctx context.Context) (bool, error) {
		sts, err := c.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		if sts.Status.ObservedGeneration < sts.Generation {
			return false, nil
		}
		return sts.Status.ReadyReplicas >= desired && sts.Status.UpdatedReplicas >= desired, nil
	})
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
