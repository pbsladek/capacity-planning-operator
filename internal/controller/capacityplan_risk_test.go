package controller

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"github.com/pbsladek/capacity-planning-operator/internal/analysis"
)

func TestBuildPVCRiskSignal_ProjectedFullAndAcceleration(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	summary := capacityv1.PVCSummary{
		Namespace:     "ns",
		Name:          "pvc-a",
		UsedBytes:     8 * 1024 * 1024 * 1024,
		CapacityBytes: 10 * 1024 * 1024 * 1024,
		UsageRatio:    0.8,
		AlertFiring:   true,
	}
	samples := []analysis.Sample{
		{Timestamp: now.Add(-7 * 24 * time.Hour), UsedBytes: 1 * 1024 * 1024 * 1024},
		{Timestamp: now.Add(-2 * 24 * time.Hour), UsedBytes: 7 * 1024 * 1024 * 1024},
		{Timestamp: now.Add(-1 * 24 * time.Hour), UsedBytes: 8 * 1024 * 1024 * 1024},
		{Timestamp: now, UsedBytes: 8 * 1024 * 1024 * 1024},
	}

	sig := buildPVCRiskSignal(now, summary, samples)
	if sig.WeeklyGrowthBytesPerDay <= 0 {
		t.Fatalf("expected positive weekly growth, got %f", sig.WeeklyGrowthBytesPerDay)
	}
	if sig.ProjectedFullAt == nil {
		t.Fatalf("expected projected full timestamp")
	}
	if sig.DaysUntilFull == nil {
		t.Fatalf("expected days until full")
	}
	if *sig.DaysUntilFull < 0 {
		t.Fatalf("expected non-negative days until full, got %f", *sig.DaysUntilFull)
	}
}

func TestBuildTopRisks_RankedByWeeklyGrowth(t *testing.T) {
	t.Parallel()

	top := buildTopRisks([]pvcRiskSignal{
		{Namespace: "ns", Name: "slow", WeeklyGrowthBytesPerDay: 100},
		{Namespace: "ns", Name: "fast", WeeklyGrowthBytesPerDay: 300},
		{Namespace: "ns", Name: "flat", WeeklyGrowthBytesPerDay: 0},
	}, 2)
	if len(top) != 2 {
		t.Fatalf("expected 2 top risks, got %d", len(top))
	}
	if top[0].Name != "fast" || top[1].Name != "slow" {
		t.Fatalf("unexpected top risk order: %#v", top)
	}
}

func TestBuildRiskDigest_IncludesDates(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	projected := metav1.NewTime(now.Add(48 * time.Hour))
	digest := buildRiskDigest(now, []capacityv1.PVCRiskSummary{
		{
			Namespace:               "ns",
			Name:                    "pvc-a",
			WeeklyGrowthBytesPerDay: 20 * 1024 * 1024 * 1024,
			ProjectedFullAt:         &projected,
			ConfidenceR2:            0.91,
		},
	})
	if !strings.Contains(digest, "ns/pvc-a") {
		t.Fatalf("expected pvc name in digest, got %q", digest)
	}
	if !strings.Contains(digest, projected.UTC().Format("2006-01-02")) {
		t.Fatalf("expected projected full date in digest, got %q", digest)
	}
}

func TestBuildPrometheusRuleUnstructured_InjectsRiskDigest(t *testing.T) {
	t.Parallel()

	plan := &capacityv1.CapacityPlan{}
	plan.Name = "cluster"
	digest := "Top PVC growth trends ..."
	obj := buildPrometheusRuleUnstructured(plan, 0.85, 7, digest)
	groups, found, err := unstructured.NestedSlice(obj.Object, "spec", "groups")
	if err != nil || !found || len(groups) == 0 {
		t.Fatalf("expected groups in PrometheusRule")
	}
	group, ok := groups[0].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected group structure")
	}
	rules, ok := group["rules"].([]interface{})
	if !ok || len(rules) == 0 {
		t.Fatalf("expected rules slice")
	}
	rule, ok := rules[0].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected rule structure")
	}
	annotations, ok := rule["annotations"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected annotations")
	}
	desc, _ := annotations["description"].(string)
	if !strings.Contains(desc, digest) {
		t.Fatalf("expected digest in alert description, got %q", desc)
	}
}

func TestBuildPrometheusRuleUnstructured_IncludesBudgetAndAnomalyAlerts(t *testing.T) {
	t.Parallel()

	plan := &capacityv1.CapacityPlan{}
	plan.Name = "cluster"
	obj := buildPrometheusRuleUnstructured(plan, 0.85, 7, "digest")

	groups, found, err := unstructured.NestedSlice(obj.Object, "spec", "groups")
	if err != nil || !found || len(groups) == 0 {
		t.Fatalf("expected groups in PrometheusRule")
	}
	group, ok := groups[0].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected group structure")
	}
	rules, ok := group["rules"].([]interface{})
	if !ok {
		t.Fatalf("expected rules slice")
	}

	alerts := map[string]bool{}
	for _, raw := range rules {
		rule, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := rule["alert"].(string)
		alerts[name] = true
	}

	for _, want := range []string{
		"PVCGrowthAccelerationSpike",
		"PVCTrendInstability",
		"NamespaceBudgetBreachSoon",
		"WorkloadBudgetBreachSoon",
	} {
		if !alerts[want] {
			t.Fatalf("expected alert %q to be generated", want)
		}
	}
}

func TestBuildPrometheusRuleUnstructured_IncludesKubePromSelectorLabels(t *testing.T) {
	t.Parallel()

	plan := &capacityv1.CapacityPlan{}
	plan.Name = "cluster"
	obj := buildPrometheusRuleUnstructured(plan, 0.85, 7, "digest")

	labels := obj.GetLabels()
	if labels["release"] != "kube-prometheus-stack" {
		t.Fatalf("expected release label for kube-prometheus-stack, got %q", labels["release"])
	}
	if labels["app.kubernetes.io/instance"] != "kube-prometheus-stack" {
		t.Fatalf("expected app.kubernetes.io/instance label for kube-prometheus-stack, got %q", labels["app.kubernetes.io/instance"])
	}
}

func TestDetectRiskChanges_NewEscalatedRecovered(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	prevProjected := metav1.NewTime(now.Add(10 * 24 * time.Hour))
	currProjected := metav1.NewTime(now.Add(5 * 24 * time.Hour))

	prev := []capacityv1.PVCRiskSummary{
		{Namespace: "ns", Name: "a", WeeklyGrowthBytesPerDay: 100, ProjectedFullAt: &prevProjected},
		{Namespace: "ns", Name: "b", WeeklyGrowthBytesPerDay: 50},
	}
	curr := []capacityv1.PVCRiskSummary{
		{Namespace: "ns", Name: "a", WeeklyGrowthBytesPerDay: 140, ProjectedFullAt: &currProjected},
		{Namespace: "ns", Name: "c", WeeklyGrowthBytesPerDay: 70},
	}

	changes := detectRiskChanges(now, prev, curr)
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes, got %d: %#v", len(changes), changes)
	}
	summary := summarizeRiskChanges(changes)
	if !strings.Contains(summary, "new=1") || !strings.Contains(summary, "escalated=1") || !strings.Contains(summary, "recovered=1") {
		t.Fatalf("unexpected summary %q", summary)
	}
}

func TestComputeRiskSnapshotHash_StableAcrossOrder(t *testing.T) {
	t.Parallel()

	r1 := []capacityv1.PVCRiskSummary{
		{Namespace: "ns", Name: "a", WeeklyGrowthBytesPerDay: 100},
		{Namespace: "ns", Name: "b", WeeklyGrowthBytesPerDay: 200},
	}
	r2 := []capacityv1.PVCRiskSummary{
		{Namespace: "ns", Name: "b", WeeklyGrowthBytesPerDay: 200},
		{Namespace: "ns", Name: "a", WeeklyGrowthBytesPerDay: 100},
	}
	h1 := computeRiskSnapshotHash(r1)
	h2 := computeRiskSnapshotHash(r2)
	if h1 == "" || h2 == "" {
		t.Fatalf("expected non-empty hashes")
	}
	if h1 != h2 {
		t.Fatalf("expected stable hash across ordering, got %q vs %q", h1, h2)
	}
}

func TestBuildBudgetForecasts_NamespaceAndWorkload(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	summaries := []capacityv1.PVCSummary{
		{Namespace: "ns1", Name: "pvc-a", UsedBytes: 8 * 1024 * 1024 * 1024},
		{Namespace: "ns1", Name: "pvc-b", UsedBytes: 2 * 1024 * 1024 * 1024},
	}
	riskByKey := map[string]pvcRiskSignal{
		"ns1/pvc-a": {WeeklyGrowthBytesPerDay: 2 * 1024 * 1024 * 1024},
		"ns1/pvc-b": {WeeklyGrowthBytesPerDay: 1 * 1024 * 1024 * 1024},
	}
	workloads := map[string]pvcWorkloadRef{
		"ns1/pvc-a": {Kind: "StatefulSet", Name: "db", Namespace: "ns1"},
		"ns1/pvc-b": {Kind: "StatefulSet", Name: "db", Namespace: "ns1"},
	}
	spec := capacityv1.BudgetSpec{
		NamespaceBudgets: []capacityv1.NamespaceBudgetSpec{
			{Namespace: "ns1", Budget: "20Gi"},
		},
		WorkloadBudgets: []capacityv1.WorkloadBudgetSpec{
			{Namespace: "ns1", Kind: "StatefulSet", Name: "db", Budget: "15Gi"},
		},
	}

	nsf, wlf := buildBudgetForecasts(now, spec, summaries, riskByKey, workloads)
	if len(nsf) != 1 || len(wlf) != 1 {
		t.Fatalf("expected one namespace and one workload forecast, got ns=%d wl=%d", len(nsf), len(wlf))
	}
	if nsf[0].DaysUntilBreach == nil {
		t.Fatalf("expected namespace days until breach")
	}
	if wlf[0].DaysUntilBreach == nil {
		t.Fatalf("expected workload days until breach")
	}
}

func TestDetectPVCAnomalies(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	signals := []pvcRiskSignal{
		{
			Namespace:               "ns",
			Name:                    "pvc-a",
			WeeklyGrowthBytesPerDay: 1 * 1024 * 1024 * 1024,
			DailyGrowthBytesPerDay:  3 * 1024 * 1024 * 1024,
			GrowthAcceleration:      2.0,
			ConfidenceR2:            0.2,
		},
	}
	workloads := map[string]pvcWorkloadRef{
		"ns/pvc-a": {Kind: "StatefulSet", Name: "db", Namespace: "ns"},
	}

	out := detectPVCAnomalies(now, signals, workloads)
	if len(out) < 2 {
		t.Fatalf("expected multiple anomalies, got %#v", out)
	}
	if summarizePVCAnomalies(out) == "" {
		t.Fatalf("expected anomaly summary")
	}
}
