/*
Copyright 2024 pbsladek.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	tutorialv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
)

// SampleReconciler reconciles a Sample object.
type SampleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=tutorial.example.com,resources=samples,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tutorial.example.com,resources=samples/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tutorial.example.com,resources=samples/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *SampleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Sample instance.
	sample := &tutorialv1.Sample{}
	if err := r.Get(ctx, req.NamespacedName, sample); err != nil {
		if errors.IsNotFound(err) {
			// Object not found; it was deleted. Stop reconciliation.
			logger.Info("Sample resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Sample")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling Sample", "name", sample.Name, "namespace", sample.Namespace)

	// TODO: Add your reconciliation logic here.
	// Example: create/update child resources, update status, etc.

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SampleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&tutorialv1.Sample{}).
		Named("sample").
		Complete(r)
}
