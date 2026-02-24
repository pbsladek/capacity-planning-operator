package cirunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

var clusterScopedKinds = map[string]bool{
	"Namespace":                      true,
	"CustomResourceDefinition":       true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"StorageClass":                   true,
	"PersistentVolume":               true,
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
}

func loadYAMLObjectsFromFile(path string) ([]*unstructured.Unstructured, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	objects := make([]*unstructured.Unstructured, 0)
	for {
		objMap := map[string]interface{}{}
		if err := decoder.Decode(&objMap); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decoding %s: %w", path, err)
		}
		if len(objMap) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{Object: objMap}
		if obj.GetKind() == "" || obj.GetAPIVersion() == "" {
			continue
		}
		objects = append(objects, obj)
	}
	return objects, nil
}

func applyNamePrefix(name, prefix string) string {
	if prefix == "" || name == "" || strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

func transformDefaultObject(obj *unstructured.Unstructured, namespace, namePrefix string) {
	kind := obj.GetKind()

	// kustomize does not prefix CRD resource names.
	if kind != "CustomResourceDefinition" {
		obj.SetName(applyNamePrefix(obj.GetName(), namePrefix))
	}

	if !clusterScopedKinds[kind] {
		obj.SetNamespace(namespace)
	}

	switch kind {
	case "RoleBinding":
		var rb rbacv1.RoleBinding
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &rb); err == nil {
			if rb.RoleRef.Kind == "Role" {
				rb.RoleRef.Name = applyNamePrefix(rb.RoleRef.Name, namePrefix)
			}
			for i := range rb.Subjects {
				if rb.Subjects[i].Kind == "ServiceAccount" {
					rb.Subjects[i].Name = applyNamePrefix(rb.Subjects[i].Name, namePrefix)
					rb.Subjects[i].Namespace = namespace
				}
			}
			if out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&rb); err == nil {
				obj.Object = out
			}
		}
	case "ClusterRoleBinding":
		var crb rbacv1.ClusterRoleBinding
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &crb); err == nil {
			if crb.RoleRef.Kind == "ClusterRole" {
				crb.RoleRef.Name = applyNamePrefix(crb.RoleRef.Name, namePrefix)
			}
			for i := range crb.Subjects {
				if crb.Subjects[i].Kind == "ServiceAccount" {
					crb.Subjects[i].Name = applyNamePrefix(crb.Subjects[i].Name, namePrefix)
					crb.Subjects[i].Namespace = namespace
				}
			}
			if out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&crb); err == nil {
				obj.Object = out
			}
		}
	case "Deployment":
		var deploy appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &deploy); err == nil {
			if deploy.Spec.Template.Spec.ServiceAccountName != "" {
				deploy.Spec.Template.Spec.ServiceAccountName = applyNamePrefix(deploy.Spec.Template.Spec.ServiceAccountName, namePrefix)
			}
			if out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&deploy); err == nil {
				obj.Object = out
			}
		}
	case "ServiceAccount":
		var sa corev1.ServiceAccount
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &sa); err == nil {
			sa.Namespace = namespace
			if out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&sa); err == nil {
				obj.Object = out
			}
		}
	}
}

func applyObject(ctx context.Context, c *Clients, mapper meta.RESTMapper, obj *unstructured.Unstructured) error {
	gvk := obj.GroupVersionKind()
	mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
	if err != nil {
		return fmt.Errorf("mapping %s: %w", gvk.String(), err)
	}

	if obj.GetNamespace() == "" && mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		// Leave empty only if resource truly defaults; all of ours should be set by transforms.
	}

	namespaceable := c.Dynamic.Resource(mapping.Resource)
	var resourceClient dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		resourceClient = namespaceable.Namespace(obj.GetNamespace())
	} else {
		resourceClient = namespaceable
	}

	current, err := resourceClient.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting %s/%s: %w", gvk.Kind, obj.GetName(), err)
		}
		_, createErr := resourceClient.Create(ctx, obj, metav1.CreateOptions{})
		if createErr != nil {
			return fmt.Errorf("creating %s/%s: %w", gvk.Kind, obj.GetName(), createErr)
		}
		return nil
	}

	obj.SetResourceVersion(current.GetResourceVersion())
	_, err = resourceClient.Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating %s/%s: %w", gvk.Kind, obj.GetName(), err)
	}
	return nil
}

func applyFiles(ctx context.Context, c *Clients, mapper meta.RESTMapper, paths []string, transform func(*unstructured.Unstructured)) error {
	for _, path := range paths {
		objs, err := loadYAMLObjectsFromFile(path)
		if err != nil {
			return err
		}
		for _, obj := range objs {
			if transform != nil {
				transform(obj)
			}
			if err := applyObject(ctx, c, mapper, obj); err != nil {
				return fmt.Errorf("applying %s: %w", path, err)
			}
		}
	}
	return nil
}

func ApplyOperatorManifests(ctx context.Context, c *Clients) error {
	mapper := c.DiscoveryMapper()
	base := "config"

	// CRDs first.
	crdFiles := []string{
		filepath.Join(base, "crd", "bases", "capacityplanning.pbsladek.io_capacityplannotifications.yaml"),
		filepath.Join(base, "crd", "bases", "capacityplanning.pbsladek.io_capacityplans.yaml"),
	}
	if err := applyFiles(ctx, c, mapper, crdFiles, nil); err != nil {
		return err
	}

	// Build equivalent of config/default kustomize transforms.
	const targetNS = "k8s-operator-system"
	const namePrefix = "k8s-operator-"
	if err := c.EnsureNamespace(ctx, targetNS); err != nil {
		return fmt.Errorf("ensuring namespace: %w", err)
	}

	files := []string{
		filepath.Join(base, "rbac", "service_account.yaml"),
		filepath.Join(base, "rbac", "leader_election_role.yaml"),
		filepath.Join(base, "rbac", "role.yaml"),
		filepath.Join(base, "rbac", "leader_election_role_binding.yaml"),
		filepath.Join(base, "rbac", "role_binding.yaml"),
		filepath.Join(base, "manager", "manager.yaml"),
		filepath.Join(base, "prometheus", "metrics_service.yaml"),
		filepath.Join(base, "prometheus", "service_monitor.yaml"),
	}
	transform := func(obj *unstructured.Unstructured) {
		transformDefaultObject(obj, targetNS, namePrefix)
	}
	return applyFiles(ctx, c, mapper, files, transform)
}

func ApplyWorkloadManifests(ctx context.Context, c *Clients, manifestDir string) error {
	if err := ApplyWorkloadStorageManifests(ctx, c, manifestDir); err != nil {
		return err
	}
	return ApplyWorkloadPodManifests(ctx, c, manifestDir)
}

func ApplyAlertReceiverManifests(ctx context.Context, c *Clients, manifestDir string) error {
	mapper := c.DiscoveryMapper()
	receiverDir := filepath.Join(manifestDir, "alert-receiver")
	files := []string{
		filepath.Join(receiverDir, "service.yaml"),
		filepath.Join(receiverDir, "deployment.yaml"),
	}
	return applyFiles(ctx, c, mapper, files, nil)
}

func ApplyWorkloadStorageManifests(ctx context.Context, c *Clients, manifestDir string) error {
	mapper := c.DiscoveryMapper()
	workloadsDir := filepath.Join(manifestDir, "workloads")
	files := []string{
		filepath.Join(workloadsDir, "pvc-steady.yaml"),
		filepath.Join(workloadsDir, "pvc-bursty.yaml"),
		filepath.Join(workloadsDir, "pvc-trickle.yaml"),
		filepath.Join(workloadsDir, "pvc-churn.yaml"),
		filepath.Join(workloadsDir, "pvc-delayed.yaml"),
	}
	return applyFiles(ctx, c, mapper, files, nil)
}

func ApplyWorkloadPodManifests(ctx context.Context, c *Clients, manifestDir string) error {
	mapper := c.DiscoveryMapper()
	workloadsDir := filepath.Join(manifestDir, "workloads")
	files := []string{
		filepath.Join(workloadsDir, "pod-steady.yaml"),
		filepath.Join(workloadsDir, "pod-bursty.yaml"),
		filepath.Join(workloadsDir, "pod-trickle.yaml"),
		filepath.Join(workloadsDir, "pod-churn.yaml"),
		filepath.Join(workloadsDir, "pod-delayed.yaml"),
	}
	return applyFiles(ctx, c, mapper, files, nil)
}

func ApplyLLMManifests(ctx context.Context, c *Clients, manifestDir string, cfg Config) error {
	if !cfg.LLMEnabled {
		return nil
	}
	if strings.TrimSpace(cfg.LLMProvider) != "ollama" {
		return nil
	}

	mapper := c.DiscoveryMapper()
	files := []string{
		filepath.Join(manifestDir, "llm", "ollama.yaml"),
	}
	transform := func(obj *unstructured.Unstructured) {
		if obj.GetKind() == "Namespace" {
			obj.SetName(cfg.LLMNamespace)
			return
		}
		obj.SetNamespace(cfg.LLMNamespace)
		if obj.GetKind() == "Deployment" && obj.GetName() != "" {
			obj.SetName(cfg.LLMDeploymentName)
		}
		if obj.GetKind() == "Service" && obj.GetName() != "" {
			obj.SetName(cfg.LLMDeploymentName)
		}
	}
	return applyFiles(ctx, c, mapper, files, transform)
}

func ApplyCapacityPlan(ctx context.Context, c *Clients, cfg Config) error {
	llmProvider := "disabled"
	if cfg.LLMEnabled {
		llmProvider = strings.TrimSpace(cfg.LLMProvider)
		if llmProvider == "" {
			llmProvider = "ollama"
		}
	}

	llmSpec := capacityv1.LLMProviderSpec{
		Provider: llmProvider,
	}
	if llmProvider != "disabled" {
		llmSpec.Model = strings.TrimSpace(cfg.LLMModel)
		if cfg.LLMTimeoutSeconds > 0 {
			llmSpec.Timeout = metav1.Duration{Duration: time.Duration(cfg.LLMTimeoutSeconds) * time.Second}
		}
		if cfg.LLMMaxTokens > 0 {
			llmSpec.MaxTokens = cfg.LLMMaxTokens
		}
		llmSpec.OnlyAlertingPVCs = cfg.LLMOnlyAlertingPVCs
		if llmProvider == "ollama" {
			llmSpec.Ollama = capacityv1.OllamaProviderSpec{
				URL: strings.TrimSpace(cfg.LLMOllamaURL),
			}
		}
	}

	plan := capacityv1.CapacityPlan{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "capacityplanning.pbsladek.io/v1",
			Kind:       "CapacityPlan",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: cfg.PlanName,
		},
		Spec: capacityv1.CapacityPlanSpec{
			Namespaces:        []string{"default"},
			SampleRetention:   cfg.PlanSampleRetention,
			PrometheusURL:     cfg.PromURL,
			ReconcileInterval: metav1.Duration{Duration: parseDurationOr(cfg.PlanReconcileInterval, 15)},
			SampleInterval:    metav1.Duration{Duration: parseDurationOr(cfg.PlanSampleInterval, 5)},
			Thresholds: capacityv1.ThresholdSpec{
				UsageRatio:    "0.03",
				DaysUntilFull: 14,
			},
			Budgets: capacityv1.BudgetSpec{
				NamespaceBudgets: []capacityv1.NamespaceBudgetSpec{
					{Namespace: "default", Budget: "1600Mi"},
				},
				WorkloadBudgets: []capacityv1.WorkloadBudgetSpec{
					{Namespace: "default", Kind: "Pod", Name: "cpo-ci-steady", Budget: "160Mi"},
					{Namespace: "default", Kind: "Pod", Name: "cpo-ci-bursty", Budget: "160Mi"},
					{Namespace: "default", Kind: "Pod", Name: "cpo-ci-trickle", Budget: "160Mi"},
					{Namespace: "default", Kind: "Pod", Name: "cpo-ci-churn", Budget: "160Mi"},
					{Namespace: "default", Kind: "Pod", Name: "cpo-ci-delayed", Budget: "160Mi"},
				},
			},
			LLM: llmSpec,
		},
	}

	key := types.NamespacedName{Name: cfg.PlanName}
	var existing capacityv1.CapacityPlan
	err := c.Controller.Get(ctx, key, &existing)
	if err == nil {
		plan.ResourceVersion = existing.ResourceVersion
		return c.Controller.Update(ctx, &plan)
	}
	if apierrors.IsNotFound(err) {
		return c.Controller.Create(ctx, &plan)
	}
	return fmt.Errorf("getting capacity plan %q: %w", cfg.PlanName, err)
}
