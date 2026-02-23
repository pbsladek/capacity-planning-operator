package cirunner

import (
	"context"
	"fmt"
	"strings"

	capacityv1 "github.com/pbsladek/capacity-planning-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type Clients struct {
	RESTConfig     *rest.Config
	Clientset      *kubernetes.Clientset
	APIExtensions  *apiextensionsclient.Clientset
	Dynamic        dynamic.Interface
	Discovery      discovery.DiscoveryInterface
	Controller     ctrlclient.Client
	CurrentContext string
}

func BuildClients() (*Clients, error) {
	rawCfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	currentContext := strings.TrimSpace(rawCfg.CurrentContext)

	restCfg, err := clientcmd.NewDefaultClientConfig(*rawCfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building rest config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating typed client: %w", err)
	}
	apiExtensions, err := apiextensionsclient.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating api extensions client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating discovery client: %w", err)
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(capacityv1.AddToScheme(scheme))
	controllerClient, err := ctrlclient.New(restCfg, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating controller client: %w", err)
	}

	return &Clients{
		RESTConfig:     restCfg,
		Clientset:      clientset,
		APIExtensions:  apiExtensions,
		Dynamic:        dyn,
		Discovery:      discoveryClient,
		Controller:     controllerClient,
		CurrentContext: currentContext,
	}, nil
}

func (c *Clients) EnsureNamespace(ctx context.Context, namespace string) error {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil
	}
	_, err := c.Clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	return retry.OnError(retry.DefaultBackoff, func(err error) bool { return true }, func() error {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		}
		_, createErr := c.Clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if createErr != nil && strings.Contains(createErr.Error(), "already exists") {
			return nil
		}
		return createErr
	})
}

func (c *Clients) DiscoveryMapper() *restmapper.DeferredDiscoveryRESTMapper {
	cache := memory.NewMemCacheClient(c.Discovery)
	return restmapper.NewDeferredDiscoveryRESTMapper(cache)
}
