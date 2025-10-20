package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorizond/capi-vip-allocator/pkg/metrics"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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
	controlPlaneRole         = "control-plane"
	ingressRole              = "ingress"
	clusterClassLabel        = "vip.capi.gorizond.io/cluster-class"
	roleLabel                = "vip.capi.gorizond.io/role"
	ingressEnabledAnnotation = "vip.capi.gorizond.io/ingress-enabled"
	ingressVipAnnotation     = "vip.capi.gorizond.io/ingress-vip"
	ipamGroup                = "ipam.cluster.x-k8s.io"
	ipamVersion              = "v1beta1"  // for IPAddressClaim and IPAddress
	globalPoolAPIVersion     = "v1alpha2" // for GlobalInClusterIPPool
	globalPoolKind           = "GlobalInClusterIPPool"
	ipAddressClaimKind       = "IPAddressClaim"
	ipAddressKind            = "IPAddress"
	defaultRequeueDelay      = 10 * time.Second
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
// This controller works as a FALLBACK for clusters created without BeforeClusterCreate hook
// or when the hook fails/is disabled.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	log := r.Logger.WithValues("cluster", req.NamespacedName)

	cluster := &clusterv1.Cluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, cluster); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetch cluster: %w", err)
	}

	// Skip if no topology (non-ClusterClass clusters)
	if cluster.Spec.Topology == nil || cluster.Spec.Topology.Class == "" {
		return ctrl.Result{}, nil
	}

	clusterClass := cluster.Spec.Topology.Class

	// Track reconcile result
	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.VipReconcileDurationSeconds.WithLabelValues(clusterClass).Observe(duration)
	}()

	// ALWAYS check and allocate Ingress VIP first (independent of Control Plane VIP)
	// Check if Ingress VIP is explicitly disabled
	if cluster.Annotations[ingressEnabledAnnotation] != "false" {
		if err := r.ensureIngressVIP(ctx, cluster, log); err != nil {
			log.Error(err, "ensure ingress VIP")
			metrics.VipAllocationErrorsTotal.WithLabelValues(ingressRole, clusterClass, "ingress_vip_allocation_failed").Inc()
			metrics.VipReconcileTotal.WithLabelValues(clusterClass, "error").Inc()
			return ctrl.Result{}, err
		}
	} else {
		log.V(1).Info("ingress VIP explicitly disabled via annotation")
	}

	// EARLY CHECK: Skip Control Plane VIP allocation if already set
	if cluster.Spec.ControlPlaneEndpoint.Host != "" {
		log.V(1).Info("controlPlaneEndpoint already set (by BeforeClusterCreate hook or manual configuration), skipping control plane VIP reconcile",
			"host", cluster.Spec.ControlPlaneEndpoint.Host)

		// Still ensure claim is adopted (ownerReference set)
		claimName := fmt.Sprintf("vip-cp-%s", cluster.Name)
		_, err := r.ensureClaim(ctx, cluster, claimName)
		if err != nil {
			// Only log error, don't block reconcile
			log.V(1).Info("could not adopt IPAddressClaim (may not exist)", "error", err.Error())
		}

		metrics.VipReconcileTotal.WithLabelValues(clusterClass, "skipped").Inc()
		return ctrl.Result{}, nil
	}

	log.Info("controlPlaneEndpoint not set, controller will allocate VIP (fallback mode)")

	allocationStart := time.Now()
	claimName := fmt.Sprintf("vip-cp-%s", cluster.Name)

	// Ensure claim exists and adopt it if needed (may have been created by runtime extension)
	claim, err := r.ensureClaim(ctx, cluster, claimName)
	if err != nil {
		log.Error(err, "ensure IPAddressClaim")
		metrics.VipAllocationErrorsTotal.WithLabelValues(controlPlaneRole, clusterClass, "claim_creation_failed").Inc()
		metrics.VipReconcileTotal.WithLabelValues(clusterClass, "error").Inc()
		return ctrl.Result{}, err
	}

	// Wait for IP allocation
	ip, ready, err := r.resolveIPAddress(ctx, cluster.Namespace, claim)
	if err != nil {
		log.Error(err, "resolve IPAddress")
		metrics.VipAllocationErrorsTotal.WithLabelValues(controlPlaneRole, clusterClass, "ip_resolution_failed").Inc()
		metrics.VipReconcileTotal.WithLabelValues(clusterClass, "error").Inc()
		return ctrl.Result{}, err
	}
	if !ready {
		log.Info("claim not ready, will requeue")
		metrics.VipReconcileTotal.WithLabelValues(clusterClass, "requeued").Inc()
		return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
	}

	// Patch cluster endpoint
	if err := r.patchClusterEndpoint(ctx, cluster, ip, cluster.Namespace); err != nil {
		log.Error(err, "patch cluster endpoint")
		metrics.VipAllocationErrorsTotal.WithLabelValues(controlPlaneRole, clusterClass, "cluster_patch_failed").Inc()
		metrics.VipReconcileTotal.WithLabelValues(clusterClass, "error").Inc()
		return ctrl.Result{}, err
	}

	allocationDuration := time.Since(allocationStart).Seconds()
	metrics.VipAllocationDurationSeconds.WithLabelValues(controlPlaneRole, clusterClass).Observe(allocationDuration)
	metrics.VipAllocationsTotal.WithLabelValues(controlPlaneRole, clusterClass).Inc()
	metrics.VipReconcileTotal.WithLabelValues(clusterClass, "success").Inc()

	log.Info("control-plane VIP assigned by controller (fallback mode)", "ip", ip, "duration_seconds", allocationDuration)

	return ctrl.Result{}, nil
}

func (r *ClusterReconciler) ensureClaim(ctx context.Context, cluster *clusterv1.Cluster, claimName string) (*unstructured.Unstructured, error) {
	log := r.Logger.WithValues("cluster", cluster.Name, "claim", claimName)
	claimGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind}

	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(claimGVK)

	namespacedName := types.NamespacedName{Name: claimName, Namespace: cluster.Namespace}
	if err := r.Client.Get(ctx, namespacedName, claim); err == nil {
		// Claim exists - check if it needs ownerReference adoption
		if len(claim.GetOwnerReferences()) == 0 {
			// Claim was created by runtime extension hook without ownerRef
			// Adopt it by adding ownerReference
			log.Info("Adopting IPAddressClaim created by runtime extension")
			ownerRef := metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind("Cluster"))
			claim.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})

			if err := r.Client.Update(ctx, claim); err != nil {
				return nil, fmt.Errorf("adopt IPAddressClaim: %w", err)
			}
			log.Info("IPAddressClaim adopted successfully")
		}
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
		"apiGroup": ipamGroup,
		"kind":     globalPoolKind,
		"name":     poolName,
	}, "spec", "poolRef"); err != nil {
		return nil, fmt.Errorf("set poolRef: %w", err)
	}

	if err := r.Client.Create(ctx, claim); err != nil {
		return nil, fmt.Errorf("create IPAddressClaim: %w", err)
	}

	return claim, nil
}

func (r *ClusterReconciler) findPool(ctx context.Context, className, role string) (string, error) {
	poolListGVK := schema.GroupVersionKind{Group: ipamGroup, Version: globalPoolAPIVersion, Kind: globalPoolKind + "List"}
	pools := &unstructured.UnstructuredList{}
	pools.SetGroupVersionKind(poolListGVK)

	// List all GlobalInClusterIPPool resources without label filtering
	// We'll filter them manually to support comma-separated values
	if err := r.Client.List(ctx, pools); err != nil {
		return "", fmt.Errorf("list %s: %w", globalPoolKind, err)
	}

	// Find a pool that matches both className and role (supporting comma-separated values)
	for _, pool := range pools.Items {
		labels := pool.GetLabels()

		// Check if cluster-class label matches (exact or comma-separated)
		classLabel, classExists := labels[clusterClassLabel]
		if !classExists {
			continue
		}
		if !labelContainsValue(classLabel, className) {
			continue
		}

		// Check if role label matches (exact or comma-separated)
		roleLabel, roleExists := labels[roleLabel]
		if !roleExists {
			continue
		}
		if !labelContainsValue(roleLabel, role) {
			continue
		}

		// Found a matching pool
		return pool.GetName(), nil
	}

	return "", nil
}

// labelContainsValue checks if a label value contains the target value.
// Supports both exact match and comma-separated lists.
// Examples:
//   - labelContainsValue("rke2-proxmox-class", "rke2-proxmox-class") -> true
//   - labelContainsValue("class1,class2,class3", "class2") -> true
//   - labelContainsValue("class1, class2, class3", "class2") -> true (with spaces)
func labelContainsValue(labelValue, targetValue string) bool {
	// Trim spaces from target value
	targetValue = strings.TrimSpace(targetValue)

	// Check exact match first (optimization)
	if strings.TrimSpace(labelValue) == targetValue {
		return true
	}

	// Split by comma and check each value
	values := strings.Split(labelValue, ",")
	for _, val := range values {
		if strings.TrimSpace(val) == targetValue {
			return true
		}
	}

	return false
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

func (r *ClusterReconciler) patchClusterEndpoint(ctx context.Context, cluster *clusterv1.Cluster, ip string, clusterNamespace string) error {
	patchHelper := client.MergeFrom(cluster.DeepCopy())

	// Set the controlPlaneEndpoint directly
	cluster.Spec.ControlPlaneEndpoint.Host = ip
	if cluster.Spec.ControlPlaneEndpoint.Port == 0 {
		cluster.Spec.ControlPlaneEndpoint.Port = r.DefaultPort
	}

	// Check if ClusterClass defines clusterVip variable (legacy mode)
	// Only patch topology.variables if the variable is defined in ClusterClass
	if cluster.Spec.Topology != nil {
		clusterClass, err := r.getClusterClass(ctx, cluster.Spec.Topology.Class, clusterNamespace)
		if err != nil {
			return fmt.Errorf("get ClusterClass: %w", err)
		}

		// Check if ClusterClass defines clusterVip variable
		if r.hasClusterVipVariable(clusterClass) {
			// Legacy mode: update or add clusterVip variable
			found := false
			for i := range cluster.Spec.Topology.Variables {
				if cluster.Spec.Topology.Variables[i].Name == "clusterVip" {
					cluster.Spec.Topology.Variables[i].Value.Raw = []byte(fmt.Sprintf("%q", ip))
					found = true
					break
				}
			}

			// If not found, append new variable
			if !found {
				cluster.Spec.Topology.Variables = append(cluster.Spec.Topology.Variables, clusterv1.ClusterVariable{
					Name:  "clusterVip",
					Value: apiextensionsv1.JSON{Raw: []byte(fmt.Sprintf("%q", ip))},
				})
			}
		}
		// If ClusterClass doesn't define clusterVip, we're in direct mode
		// Only controlPlaneEndpoint.Host is patched (lines 205-208)
	}

	if err := r.Client.Patch(ctx, cluster, patchHelper); err != nil {
		return fmt.Errorf("patch cluster endpoint: %w", err)
	}

	return nil
}

// getClusterClass fetches the ClusterClass for the given class name.
// First tries to get it as cluster-scoped, then falls back to namespace-scoped.
func (r *ClusterReconciler) getClusterClass(ctx context.Context, className string, clusterNamespace string) (*clusterv1.ClusterClass, error) {
	clusterClass := &clusterv1.ClusterClass{}

	// First try cluster-scoped (without namespace)
	namespacedName := types.NamespacedName{Name: className}
	err := r.Client.Get(ctx, namespacedName, clusterClass)
	if err == nil {
		return clusterClass, nil
	}

	// If not found, try namespace-scoped (with cluster's namespace)
	if errors.IsNotFound(err) {
		namespacedName = types.NamespacedName{Name: className, Namespace: clusterNamespace}
		if err := r.Client.Get(ctx, namespacedName, clusterClass); err != nil {
			return nil, fmt.Errorf("get ClusterClass %q (tried both cluster-scoped and namespace %q): %w", className, clusterNamespace, err)
		}
		return clusterClass, nil
	}

	return nil, fmt.Errorf("get ClusterClass %q: %w", className, err)
}

// hasClusterVipVariable checks if the ClusterClass defines a clusterVip variable.
func (r *ClusterReconciler) hasClusterVipVariable(clusterClass *clusterv1.ClusterClass) bool {
	for _, variable := range clusterClass.Spec.Variables {
		if variable.Name == "clusterVip" {
			return true
		}
	}
	return false
}

// ensureIngressVIP allocates and sets Ingress VIP annotation for the cluster.
func (r *ClusterReconciler) ensureIngressVIP(ctx context.Context, cluster *clusterv1.Cluster, log logr.Logger) error {
	clusterClass := cluster.Spec.Topology.Class

	// Check if ingress VIP annotation already set
	if existingVip, ok := cluster.Annotations[ingressVipAnnotation]; ok && existingVip != "" {
		log.V(1).Info("ingress VIP annotation already set, skipping allocation", "vip", existingVip)
		return nil
	}

	allocationStart := time.Now()
	claimName := fmt.Sprintf("vip-ingress-%s", cluster.Name)

	// Ensure claim exists
	claim, err := r.ensureClaimWithRole(ctx, cluster, claimName, ingressRole)
	if err != nil {
		metrics.VipAllocationErrorsTotal.WithLabelValues(ingressRole, clusterClass, "claim_creation_failed").Inc()
		return fmt.Errorf("ensure ingress IPAddressClaim: %w", err)
	}

	// Wait for IP allocation
	ip, ready, err := r.resolveIPAddress(ctx, cluster.Namespace, claim)
	if err != nil {
		metrics.VipAllocationErrorsTotal.WithLabelValues(ingressRole, clusterClass, "ip_resolution_failed").Inc()
		return fmt.Errorf("resolve ingress IPAddress: %w", err)
	}
	if !ready {
		log.Info("ingress claim not ready, will requeue")
		return nil
	}

	// Set ingress VIP in annotation and label
	patchHelper := client.MergeFrom(cluster.DeepCopy())

	if cluster.Annotations == nil {
		cluster.Annotations = make(map[string]string)
	}
	cluster.Annotations[ingressVipAnnotation] = ip

	if cluster.Labels == nil {
		cluster.Labels = make(map[string]string)
	}
	cluster.Labels[ingressVipAnnotation] = ip

	if err := r.Client.Patch(ctx, cluster, patchHelper); err != nil {
		metrics.VipAllocationErrorsTotal.WithLabelValues(ingressRole, clusterClass, "cluster_patch_failed").Inc()
		return fmt.Errorf("patch cluster ingress VIP annotation and label: %w", err)
	}

	allocationDuration := time.Since(allocationStart).Seconds()
	metrics.VipAllocationDurationSeconds.WithLabelValues(ingressRole, clusterClass).Observe(allocationDuration)
	metrics.VipAllocationsTotal.WithLabelValues(ingressRole, clusterClass).Inc()

	log.Info("ingress VIP assigned to annotation and label", "ip", ip, "annotation", ingressVipAnnotation, "duration_seconds", allocationDuration)
	return nil
}

// ensureClaim creates or adopts an IPAddressClaim with the specified role.
// Overloaded version that accepts role parameter.
func (r *ClusterReconciler) ensureClaimWithRole(ctx context.Context, cluster *clusterv1.Cluster, claimName string, role string) (*unstructured.Unstructured, error) {
	log := r.Logger.WithValues("cluster", cluster.Name, "claim", claimName, "role", role)
	claimGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind}

	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(claimGVK)

	namespacedName := types.NamespacedName{Name: claimName, Namespace: cluster.Namespace}
	if err := r.Client.Get(ctx, namespacedName, claim); err == nil {
		// Claim exists - check if it needs ownerReference adoption
		if len(claim.GetOwnerReferences()) == 0 {
			log.Info("Adopting IPAddressClaim created by runtime extension")
			ownerRef := metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind("Cluster"))
			claim.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})

			if err := r.Client.Update(ctx, claim); err != nil {
				return nil, fmt.Errorf("adopt IPAddressClaim: %w", err)
			}
			log.Info("IPAddressClaim adopted successfully")
		}
		return claim, nil
	} else if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("get IPAddressClaim: %w", err)
	}

	poolName, err := r.findPool(ctx, cluster.Spec.Topology.Class, role)
	if err != nil {
		return nil, err
	}

	if poolName == "" {
		return nil, fmt.Errorf("no matching ip pool for class %q role %q", cluster.Spec.Topology.Class, role)
	}

	claim.SetName(claimName)
	claim.SetNamespace(cluster.Namespace)
	claim.SetLabels(map[string]string{
		roleLabel: role,
	})

	ownerRef := metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind("Cluster"))
	claim.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})

	if err := unstructured.SetNestedField(claim.Object, map[string]interface{}{
		"apiGroup": ipamGroup,
		"kind":     globalPoolKind,
		"name":     poolName,
	}, "spec", "poolRef"); err != nil {
		return nil, fmt.Errorf("set poolRef: %w", err)
	}

	if err := r.Client.Create(ctx, claim); err != nil {
		return nil, fmt.Errorf("create IPAddressClaim: %w", err)
	}

	return claim, nil
}
