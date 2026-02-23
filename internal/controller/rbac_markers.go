package controller

// RBAC markers are centralized here to ensure `make manifests` consistently
// generates permissions for the active controllers.

// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=capacityplanning.pbsladek.io,resources=capacityplans/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=prometheusrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
