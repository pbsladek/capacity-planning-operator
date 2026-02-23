package civerify

import (
	"context"
	"fmt"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewKubeClient creates a controller-runtime client configured from the ambient kubeconfig.
func NewKubeClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(capacityv1.AddToScheme(scheme))

	kubeCfg := ctrl.GetConfigOrDie()
	return client.New(kubeCfg, client.Options{Scheme: scheme})
}

// LoadCapacityPlanPVCGrowth loads status.pvcs growth values from a CapacityPlan.
func LoadCapacityPlanPVCGrowth(ctx context.Context, k8sClient client.Client, planName string) ([]PVCGrowth, error) {
	var cp capacityv1.CapacityPlan
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: planName}, &cp); err != nil {
		return nil, fmt.Errorf("getting capacityplan %q: %w", planName, err)
	}
	if len(cp.Status.PVCs) == 0 {
		return nil, fmt.Errorf("cannot cross-check growth math: status.pvcs is empty")
	}

	out := make([]PVCGrowth, 0, len(cp.Status.PVCs))
	for _, pvc := range cp.Status.PVCs {
		if pvc.Name == "" {
			continue
		}
		out = append(out, PVCGrowth{
			Name:              pvc.Name,
			StatusBytesPerDay: pvc.GrowthBytesPerDay,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cannot cross-check growth math: status.pvcs is empty")
	}
	return out, nil
}
