package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	controlPlaneRole    = "control-plane"
	clusterClassLabel   = "vip.capi.gorizond.io/cluster-class"
	roleLabel           = "vip.capi.gorizond.io/role"
	ipamGroup           = "ipam.cluster.x-k8s.io"
	ipamVersion         = "v1alpha2"
	globalPoolKind      = "GlobalInClusterIPPool"
	ipAddressClaimKind  = "IPAddressClaim"
	ipAddressKind       = "IPAddress"
	defaultRequeueDelay = 10 * time.Second
)

// ClusterReconciler reconciles Cluster resources to ensure a control-plane VIP is allocated.
type ClusterReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Logger      logr.Logger
	DefaultPort int32
}

// SetupWithManager wires the reconciler into controller-runtime.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.DefaultPort == 0 {
		r.DefaultPort = 6443
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1.Cluster{}).
		Complete(r)
}

// Reconcile ensures the Cluster has a VIP allocated for its control-plane endpoint.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Logger.WithValues("cluster", req.NamespacedName)

	cluster := &clusterv1.Cluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, cluster); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetch cluster: %w", err)
	}

	if cluster.Spec.Topology == nil || cluster.Spec.Topology.Class == "" {
		// Nothing to do for template-less clusters yet.
		return ctrl.Result{}, nil
	}

	if cluster.Spec.ControlPlaneEndpoint.Host != "" {
		return ctrl.Result{}, nil
	}

	claimName := fmt.Sprintf("vip-cp-%s", cluster.Name)

	claim, err := r.ensureClaim(ctx, cluster, claimName)
	if err != nil {
		log.Error(err, "ensure IPAddressClaim")
		return ctrl.Result{}, err
	}

	ip, ready, err := r.resolveIPAddress(ctx, cluster.Namespace, claim)
	if err != nil {
		log.Error(err, "resolve IPAddress")
		return ctrl.Result{}, err
	}
	if !ready {
		log.Info("claim not ready, will requeue")
		return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
	}

	if err := r.patchClusterEndpoint(ctx, cluster, ip); err != nil {
		log.Error(err, "patch cluster endpoint")
		return ctrl.Result{}, err
	}

	log.Info("control-plane VIP assigned", "ip", ip)
	return ctrl.Result{}, nil
}

func (r *ClusterReconciler) ensureClaim(ctx context.Context, cluster *clusterv1.Cluster, claimName string) (*unstructured.Unstructured, error) {
	claimGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind}

	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(claimGVK)

	namespacedName := types.NamespacedName{Name: claimName, Namespace: cluster.Namespace}
	if err := r.Client.Get(ctx, namespacedName, claim); err == nil {
		return claim, nil
	} else if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("get IPAddressClaim: %w", err)
	}

	poolName, err := r.findPool(ctx, cluster.Spec.Topology.Class, controlPlaneRole)
	if err != nil {
		return nil, err
	}

	if poolName == "" {
		return nil, fmt.Errorf("no matching ip pool for class %q", cluster.Spec.Topology.Class)
	}

	claim.SetName(claimName)
	claim.SetNamespace(cluster.Namespace)
	claim.SetLabels(map[string]string{
		roleLabel: controlPlaneRole,
	})

	ownerRef := metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind("Cluster"))
	claim.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})

	if err := unstructured.SetNestedField(claim.Object, map[string]interface{}{
		"apiVersion": fmt.Sprintf("%s/%s", ipamGroup, ipamVersion),
		"kind":       globalPoolKind,
		"name":       poolName,
	}, "spec", "poolRef"); err != nil {
		return nil, fmt.Errorf("set poolRef: %w", err)
	}

	if err := r.Client.Create(ctx, claim); err != nil {
		return nil, fmt.Errorf("create IPAddressClaim: %w", err)
	}

	return claim, nil
}

func (r *ClusterReconciler) findPool(ctx context.Context, className, role string) (string, error) {
	poolListGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: globalPoolKind + "List"}
	pools := &unstructured.UnstructuredList{}
	pools.SetGroupVersionKind(poolListGVK)

	selector := client.MatchingLabels(map[string]string{
		clusterClassLabel: className,
		roleLabel:         role,
	})

	if err := r.Client.List(ctx, pools, selector); err != nil {
		return "", fmt.Errorf("list %s: %w", globalPoolKind, err)
	}

	if len(pools.Items) == 0 {
		return "", nil
	}

	return pools.Items[0].GetName(), nil
}

func (r *ClusterReconciler) resolveIPAddress(ctx context.Context, namespace string, claim *unstructured.Unstructured) (string, bool, error) {
	addressName, found, err := unstructured.NestedString(claim.Object, "status", "addressRef", "name")
	if err != nil {
		return "", false, fmt.Errorf("read claim status: %w", err)
	}
	if !found || addressName == "" {
		return "", false, nil
	}

	ipGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressKind}
	ip := &unstructured.Unstructured{}
	ip.SetGroupVersionKind(ipGVK)

	nn := types.NamespacedName{Name: addressName, Namespace: namespace}
	if err := r.Client.Get(ctx, nn, ip); err != nil {
		if errors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get IPAddress: %w", err)
	}

	address, found, err := unstructured.NestedString(ip.Object, "spec", "address")
	if err != nil {
		return "", false, fmt.Errorf("read IPAddress: %w", err)
	}
	if !found || address == "" {
		return "", false, nil
	}

	return address, true, nil
}

func (r *ClusterReconciler) patchClusterEndpoint(ctx context.Context, cluster *clusterv1.Cluster, ip string) error {
	patchHelper := client.MergeFrom(cluster.DeepCopy())

	cluster.Spec.ControlPlaneEndpoint.Host = ip
	if cluster.Spec.ControlPlaneEndpoint.Port == 0 {
		cluster.Spec.ControlPlaneEndpoint.Port = r.DefaultPort
	}

	if err := r.Client.Patch(ctx, cluster, patchHelper); err != nil {
		return fmt.Errorf("patch cluster endpoint: %w", err)
	}

	return nil
}
