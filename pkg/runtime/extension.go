package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	runtimehooksv1 "sigs.k8s.io/cluster-api/exp/runtime/hooks/api/v1alpha1"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ipamGroup            = "ipam.cluster.x-k8s.io"
	ipamVersion          = "v1beta1"
	globalPoolAPIVersion = "v1alpha2"
	globalPoolKind       = "GlobalInClusterIPPool"
	ipAddressClaimKind   = "IPAddressClaim"
	ipAddressKind        = "IPAddress"
	clusterClassLabel    = "vip.capi.gorizond.io/cluster-class"
	roleLabel            = "vip.capi.gorizond.io/role"
	controlPlaneRole     = "control-plane"
	defaultPort          = int32(6443)

	// IP allocation retry settings for GeneratePatches hook
	ipAllocationTimeout  = 25 * time.Second // Must be less than hook timeout (30s)
	ipAllocationInterval = 500 * time.Millisecond

	// BeforeClusterCreate hook timeout settings
	beforeCreateIPTimeout  = 25 * time.Second // Must be less than hook timeout (30s max allowed by CAPI)
	beforeCreateIPInterval = 1 * time.Second  // Slightly longer interval for this hook
)

// VIPExtension implements CAPI Runtime Extension for VIP allocation.
type VIPExtension struct {
	Client        client.Client
	Logger        logr.Logger
	ExtensionName string
}

// NewVIPExtension creates a new VIP runtime extension.
func NewVIPExtension(client client.Client, logger logr.Logger, extensionName string) *VIPExtension {
	if extensionName == "" {
		extensionName = "vip-allocator" // Default name without dots
	}
	return &VIPExtension{
		Client:        client,
		Logger:        logger,
		ExtensionName: extensionName,
	}
}

// Name returns the name of the extension.
func (e *VIPExtension) Name() string {
	return e.ExtensionName
}

// GeneratePatches is called during Cluster topology reconciliation to generate patches.
// v0.3.0: This hook ONLY patches InfrastructureCluster objects with VIP already allocated by BeforeClusterCreate.
// It does NOT allocate VIPs - that's done synchronously in BeforeClusterCreate hook.
func (e *VIPExtension) GeneratePatches(ctx context.Context, request *runtimehooksv1.GeneratePatchesRequest, response *runtimehooksv1.GeneratePatchesResponse) {
	log := e.Logger.WithName("GeneratePatches")

	log.Info("GeneratePatches hook called", "itemsCount", len(request.Items))

	// Map to store VIPs from Cluster objects: clusterName -> IP
	// VIP should already be allocated by BeforeClusterCreate hook
	allocatedIPs := make(map[string]string)

	// Map to store cluster namespace: clusterName -> namespace
	clusterNamespaces := make(map[string]string)

	// First pass: Extract VIPs from Cluster objects (already allocated by BeforeClusterCreate)
	for i, item := range request.Items {
		// Check object type
		var typeMeta metav1.TypeMeta
		if err := json.Unmarshal(item.Object.Raw, &typeMeta); err != nil {
			log.Error(err, "failed to unmarshal TypeMeta", "itemIndex", i)
			continue
		}

		log.V(1).Info("processing item", "itemIndex", i, "kind", typeMeta.Kind, "apiVersion", typeMeta.APIVersion, "uid", item.UID)

		// We only care about Cluster objects
		if typeMeta.Kind != "Cluster" {
			continue
		}

		// Convert to Cluster
		cluster := &clusterv1.Cluster{}
		if err := json.Unmarshal(item.Object.Raw, cluster); err != nil {
			log.Error(err, "failed to unmarshal Cluster")
			response.SetStatus(runtimehooksv1.ResponseStatusFailure)
			response.SetMessage(fmt.Sprintf("failed to unmarshal Cluster: %v", err))
			return
		}

		log.Info("processing cluster", "name", cluster.Name, "namespace", cluster.Namespace, "uid", item.UID)

		// Store cluster namespace for later lookup
		clusterNamespaces[cluster.Name] = cluster.Namespace

		// Extract VIP from Cluster.Spec.ControlPlaneEndpoint.Host
		// VIP should already be set by BeforeClusterCreate hook
		if cluster.Spec.ControlPlaneEndpoint.Host != "" {
			log.Info("found VIP in cluster (set by BeforeClusterCreate)", "cluster", cluster.Name, "host", cluster.Spec.ControlPlaneEndpoint.Host)
			allocatedIPs[cluster.Name] = cluster.Spec.ControlPlaneEndpoint.Host
		} else {
			log.Info("no VIP in cluster - BeforeClusterCreate hook might have been skipped", "cluster", cluster.Name)
		}
	}

	// Second pass: Patch InfrastructureCluster objects with allocated VIPs
	for i, item := range request.Items {
		var typeMeta metav1.TypeMeta
		if err := json.Unmarshal(item.Object.Raw, &typeMeta); err != nil {
			log.V(1).Info("failed to unmarshal TypeMeta in second pass", "itemIndex", i, "error", err)
			continue
		}

		// Check if this is an InfrastructureCluster (any kind ending with "Cluster" in infrastructure group)
		if typeMeta.Kind == "Cluster" || !isInfrastructureCluster(typeMeta) {
			log.V(1).Info("skipping non-InfrastructureCluster", "itemIndex", i, "kind", typeMeta.Kind)
			continue
		}

		// Parse object to get metadata
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(item.Object.Raw, obj); err != nil {
			log.Error(err, "failed to unmarshal InfrastructureCluster", "kind", typeMeta.Kind, "itemIndex", i)
			continue
		}

		log.Info("found InfrastructureCluster", "kind", typeMeta.Kind, "name", obj.GetName(), "namespace", obj.GetNamespace(), "uid", item.UID)

		// Get cluster name from HolderReference (this is the Cluster that owns this object)
		// HolderReference points to the Cluster object in topology reconciliation
		var clusterName string
		if item.HolderReference.Name != "" {
			clusterName = item.HolderReference.Name
			log.Info("using HolderReference for cluster name", "infraCluster", obj.GetName(), "clusterName", clusterName)
		} else {
			// Fallback: try to extract from object name (usually matches cluster name)
			clusterName = extractClusterName(obj.GetName())
			log.Info("using extracted cluster name from object name", "infraCluster", obj.GetName(), "clusterName", clusterName)
		}

		if clusterName == "" {
			log.Info("could not determine cluster name, skipping", "infraCluster", obj.GetName())
			continue
		}

		// Check if we have allocated IP for this cluster
		ip, exists := allocatedIPs[clusterName]
		if !exists {
			log.Info("no VIP allocated for this cluster yet, skipping patch", "infraCluster", obj.GetName(), "clusterName", clusterName)
			continue
		}

		log.Info("patching InfrastructureCluster with VIP", "kind", typeMeta.Kind, "name", obj.GetName(), "clusterName", clusterName, "ip", ip)

		// Check if controlPlaneEndpoint exists in spec
		spec, found, _ := unstructured.NestedMap(obj.Object, "spec")
		if !found {
			log.Info("spec not found in InfrastructureCluster, skipping", "infraCluster", obj.GetName())
			continue
		}

		// Check if controlPlaneEndpoint field exists
		if _, exists := spec["controlPlaneEndpoint"]; exists {
			// Add patch to set controlPlaneEndpoint
			e.addGenericPatch(response, item.UID, "/spec/controlPlaneEndpoint", map[string]interface{}{
				"host": ip,
				"port": defaultPort,
			})
			log.Info("added patch for InfrastructureCluster", "infraCluster", obj.GetName(), "path", "/spec/controlPlaneEndpoint", "ip", ip)
		} else {
			log.Info("controlPlaneEndpoint field not found in spec, skipping", "infraCluster", obj.GetName())
		}
	}

	response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
}

// BeforeClusterCreate is called before a Cluster is created.
// This hook synchronously allocates a VIP and sets it in Cluster.Spec.ControlPlaneEndpoint
// BEFORE the cluster object is persisted to etcd.
func (e *VIPExtension) BeforeClusterCreate(ctx context.Context, request *runtimehooksv1.BeforeClusterCreateRequest, response *runtimehooksv1.BeforeClusterCreateResponse) {
	log := e.Logger.WithValues("cluster", types.NamespacedName{
		Name:      request.Cluster.Name,
		Namespace: request.Cluster.Namespace,
	})

	log.Info("BeforeClusterCreate hook called")

	// Skip if VIP already set (manual configuration)
	if request.Cluster.Spec.ControlPlaneEndpoint.Host != "" {
		log.Info("controlPlaneEndpoint already set, skipping VIP allocation", "host", request.Cluster.Spec.ControlPlaneEndpoint.Host)
		response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
		return
	}

	// Skip if no topology
	if request.Cluster.Spec.Topology == nil || request.Cluster.Spec.Topology.Class == "" {
		log.Info("no topology defined, skipping VIP allocation")
		response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
		return
	}

	// 1. Find the IP pool for this cluster class
	poolName, err := e.findPool(ctx, request.Cluster.Spec.Topology.Class, controlPlaneRole)
	if err != nil {
		log.Error(err, "failed to find IP pool")
		response.SetStatus(runtimehooksv1.ResponseStatusFailure)
		response.SetMessage(fmt.Sprintf("failed to find IP pool for cluster class %q: %v", request.Cluster.Spec.Topology.Class, err))
		return
	}

	if poolName == "" {
		// No pool found - this is an error in v0.3.0+
		// User must explicitly configure IP pool with proper labels
		log.Error(fmt.Errorf("no IP pool found"), "IP pool not found for cluster class", "clusterClass", request.Cluster.Spec.Topology.Class, "role", controlPlaneRole)
		response.SetStatus(runtimehooksv1.ResponseStatusFailure)
		response.SetMessage(fmt.Sprintf("no IP pool found for cluster class %q with labels vip.capi.gorizond.io/cluster-class=%s and vip.capi.gorizond.io/role=%s", request.Cluster.Spec.Topology.Class, request.Cluster.Spec.Topology.Class, controlPlaneRole))
		return
	}

	log.Info("found IP pool for VIP allocation", "pool", poolName)

	// 2. Ensure IPAddressClaim exists (create if needed)
	claimName := fmt.Sprintf("vip-cp-%s", request.Cluster.Name)
	claim, err := e.ensureIPAddressClaimForBeforeCreate(ctx, &request.Cluster, claimName, poolName)
	if err != nil {
		log.Error(err, "failed to ensure IPAddressClaim")
		response.SetStatus(runtimehooksv1.ResponseStatusFailure)
		response.SetMessage(fmt.Sprintf("failed to create IPAddressClaim: %v", err))
		return
	}

	// 3. Wait for VIP allocation from IPAM
	vip, err := e.waitForVIPInBeforeCreate(ctx, request.Cluster.Namespace, claim)
	if err != nil {
		if wait.Interrupted(err) {
			// Timeout - request retry
			log.Info("VIP allocation timeout, requesting retry")
			response.SetStatus(runtimehooksv1.ResponseStatusFailure)
			response.SetMessage(fmt.Sprintf("VIP allocation timeout after %v - will retry", beforeCreateIPTimeout))
			response.RetryAfterSeconds = int32(5) // Retry after 5 seconds
			return
		}
		log.Error(err, "failed to allocate VIP")
		response.SetStatus(runtimehooksv1.ResponseStatusFailure)
		response.SetMessage(fmt.Sprintf("failed to allocate VIP: %v", err))
		return
	}

	// 4. Set VIP in cluster object BEFORE it's created
	log.Info("VIP allocated successfully, setting controlPlaneEndpoint", "vip", vip)
	request.Cluster.Spec.ControlPlaneEndpoint.Host = vip
	request.Cluster.Spec.ControlPlaneEndpoint.Port = defaultPort

	log.Info("VIP set in BeforeClusterCreate hook - cluster will be created with this endpoint", "vip", vip, "port", defaultPort)
	response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
}

// AfterClusterUpgrade is called after a Cluster is upgraded (no-op for us).
func (e *VIPExtension) AfterClusterUpgrade(ctx context.Context, request *runtimehooksv1.AfterClusterUpgradeRequest, response *runtimehooksv1.AfterClusterUpgradeResponse) {
	response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
}

// BeforeClusterDelete is called before a Cluster is deleted (cleanup handled by ownerReferences).
func (e *VIPExtension) BeforeClusterDelete(ctx context.Context, request *runtimehooksv1.BeforeClusterDeleteRequest, response *runtimehooksv1.BeforeClusterDeleteResponse) {
	log := e.Logger.WithValues("cluster", types.NamespacedName{
		Name:      request.Cluster.Name,
		Namespace: request.Cluster.Namespace,
	})

	log.Info("BeforeClusterDelete hook called - IPAddressClaim will be cleaned up via ownerReferences")
	response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
}

func (e *VIPExtension) getVariableValueFromList(variables []runtimehooksv1.Variable, varName string) string {
	for _, v := range variables {
		if v.Name == varName {
			// Parse JSON value
			var value string
			if err := json.Unmarshal(v.Value.Raw, &value); err == nil {
				return value
			}
		}
	}
	return ""
}

func (e *VIPExtension) getVariableValue(cluster *clusterv1.Cluster, varName string) string {
	if cluster.Spec.Topology == nil {
		return ""
	}

	for _, v := range cluster.Spec.Topology.Variables {
		if v.Name == varName {
			// Parse JSON value
			var value string
			if err := json.Unmarshal(v.Value.Raw, &value); err == nil {
				return value
			}
		}
	}
	return ""
}

func (e *VIPExtension) findPool(ctx context.Context, className, role string) (string, error) {
	poolListGVK := schema.GroupVersionKind{Group: ipamGroup, Version: globalPoolAPIVersion, Kind: globalPoolKind + "List"}
	pools := &unstructured.UnstructuredList{}
	pools.SetGroupVersionKind(poolListGVK)

	selector := client.MatchingLabels(map[string]string{
		clusterClassLabel: className,
		roleLabel:         role,
	})

	if err := e.Client.List(ctx, pools, selector); err != nil {
		return "", fmt.Errorf("list %s: %w", globalPoolKind, err)
	}

	if len(pools.Items) == 0 {
		return "", nil
	}

	return pools.Items[0].GetName(), nil
}

func (e *VIPExtension) preallocateIP(ctx context.Context, cluster *clusterv1.Cluster, claimName, poolName string) (string, error) {
	log := e.Logger.WithValues("cluster", cluster.Name, "namespace", cluster.Namespace, "claim", claimName, "pool", poolName)

	// Check if claim already exists
	claimGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind}
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(claimGVK)

	namespacedName := types.NamespacedName{Name: claimName, Namespace: cluster.Namespace}

	// Try to get existing claim
	err := e.Client.Get(ctx, namespacedName, claim)
	if err == nil {
		// Claim exists, check if IP is ready
		log.Info("IPAddressClaim already exists, checking for allocated IP")
		return e.waitForIPAllocation(ctx, cluster.Namespace, namespacedName, claim)
	}

	if !errors.IsNotFound(err) {
		// Unexpected error
		log.Error(err, "failed to get IPAddressClaim")
		return "", fmt.Errorf("get IPAddressClaim: %w", err)
	}

	// Create new claim (without ownerReference - Cluster doesn't exist in etcd yet!)
	log.Info("IPAddressClaim not found, creating new one")
	claim.SetName(claimName)
	claim.SetNamespace(cluster.Namespace)
	claim.SetLabels(map[string]string{
		roleLabel: controlPlaneRole,
		// Add cluster name for later adoption by reconciler
		"cluster.x-k8s.io/cluster-name": cluster.Name,
	})

	if err := unstructured.SetNestedField(claim.Object, map[string]interface{}{
		"apiGroup": ipamGroup,
		"kind":     globalPoolKind,
		"name":     poolName,
	}, "spec", "poolRef"); err != nil {
		return "", fmt.Errorf("set poolRef: %w", err)
	}

	if err := e.Client.Create(ctx, claim); err != nil {
		if errors.IsAlreadyExists(err) {
			// Race condition: another reconciler created it
			log.Info("IPAddressClaim was created by another process, fetching it")
			return e.waitForIPAllocation(ctx, cluster.Namespace, namespacedName, nil)
		}
		log.Error(err, "failed to create IPAddressClaim")
		return "", fmt.Errorf("create IPAddressClaim: %w", err)
	}

	log.Info("IPAddressClaim created successfully, waiting for IP allocation")
	// Wait for IP to be allocated with retry
	return e.waitForIPAllocation(ctx, cluster.Namespace, namespacedName, nil)
}

// waitForIPAllocation waits for IP to be allocated to the claim with retry logic.
func (e *VIPExtension) waitForIPAllocation(ctx context.Context, namespace string, namespacedName types.NamespacedName, existingClaim *unstructured.Unstructured) (string, error) {
	log := e.Logger.WithValues("claim", namespacedName.Name, "namespace", namespace)

	claimGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind}

	var allocatedIP string
	err := wait.PollUntilContextTimeout(ctx, ipAllocationInterval, ipAllocationTimeout, true, func(ctx context.Context) (bool, error) {
		claim := existingClaim
		if claim == nil {
			claim = &unstructured.Unstructured{}
			claim.SetGroupVersionKind(claimGVK)
			if err := e.Client.Get(ctx, namespacedName, claim); err != nil {
				if errors.IsNotFound(err) {
					log.V(1).Info("IPAddressClaim not found yet, retrying")
					return false, nil // Retry
				}
				return false, err // Permanent error
			}
		}

		// Try to get IP from claim
		ip, err := e.getIPFromClaim(ctx, namespace, claim)
		if err != nil {
			log.V(1).Info("IP not ready yet, retrying", "error", err.Error())
			// Reset claim for next iteration to force refresh
			existingClaim = nil
			return false, nil // Retry
		}

		allocatedIP = ip
		log.Info("IP successfully allocated", "ip", allocatedIP)
		return true, nil // Success
	})

	if err != nil {
		if wait.Interrupted(err) {
			return "", fmt.Errorf("timeout waiting for IP allocation after %v", ipAllocationTimeout)
		}
		return "", fmt.Errorf("error waiting for IP allocation: %w", err)
	}

	return allocatedIP, nil
}

func (e *VIPExtension) getIPFromClaim(ctx context.Context, namespace string, claim *unstructured.Unstructured) (string, error) {
	addressName, found, err := unstructured.NestedString(claim.Object, "status", "addressRef", "name")
	if err != nil {
		return "", fmt.Errorf("read claim status: %w", err)
	}
	if !found || addressName == "" {
		return "", fmt.Errorf("IP not allocated yet (claim is pending)")
	}

	ipGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressKind}
	ipAddr := &unstructured.Unstructured{}
	ipAddr.SetGroupVersionKind(ipGVK)

	if err := e.Client.Get(ctx, types.NamespacedName{Name: addressName, Namespace: namespace}, ipAddr); err != nil {
		return "", fmt.Errorf("get IPAddress: %w", err)
	}

	address, found, err := unstructured.NestedString(ipAddr.Object, "spec", "address")
	if err != nil || !found || address == "" {
		return "", fmt.Errorf("IP address not found in IPAddress resource")
	}

	return address, nil
}

func (e *VIPExtension) addClusterPatch(response *runtimehooksv1.GeneratePatchesResponse, itemUID types.UID, path string, value interface{}) {
	patch := runtimehooksv1.GeneratePatchesResponseItem{
		UID:       itemUID,
		PatchType: runtimehooksv1.JSONPatchType,
		Patch: mustMarshalJSON([]map[string]interface{}{
			{
				"op":    "add",
				"path":  path,
				"value": value,
			},
		}),
	}
	response.Items = append(response.Items, patch)
}

func (e *VIPExtension) addClusterVariablePatch(response *runtimehooksv1.GeneratePatchesResponse, itemUID types.UID, cluster *clusterv1.Cluster, varName, value string) {
	// Check if variable already exists
	variableIndex := -1
	if cluster.Spec.Topology != nil {
		for i, v := range cluster.Spec.Topology.Variables {
			if v.Name == varName {
				variableIndex = i
				break
			}
		}
	}

	var jsonPatch []map[string]interface{}
	if variableIndex >= 0 {
		// Replace existing variable
		jsonPatch = []map[string]interface{}{
			{
				"op":    "replace",
				"path":  fmt.Sprintf("/spec/topology/variables/%d/value", variableIndex),
				"value": value,
			},
		}
	} else {
		// Add new variable
		jsonPatch = []map[string]interface{}{
			{
				"op":   "add",
				"path": "/spec/topology/variables/-",
				"value": map[string]interface{}{
					"name":  varName,
					"value": value,
				},
			},
		}
	}

	patch := runtimehooksv1.GeneratePatchesResponseItem{
		UID:       itemUID,
		PatchType: runtimehooksv1.JSONPatchType,
		Patch:     mustMarshalJSON(jsonPatch),
	}
	response.Items = append(response.Items, patch)
}

func mustMarshalJSON(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// addGenericPatch adds a patch for any object type (not just Cluster).
// Uses "replace" operation to handle both new and existing fields.
func (e *VIPExtension) addGenericPatch(response *runtimehooksv1.GeneratePatchesResponse, itemUID types.UID, path string, value interface{}) {
	// Use "replace" instead of "add" - it works for both existing and new fields
	// in the context of strategic merge patch
	patch := runtimehooksv1.GeneratePatchesResponseItem{
		UID:       itemUID,
		PatchType: runtimehooksv1.JSONPatchType,
		Patch: mustMarshalJSON([]map[string]interface{}{
			{
				"op":    "replace",
				"path":  path,
				"value": value,
			},
		}),
	}
	response.Items = append(response.Items, patch)
}

// isInfrastructureCluster checks if the TypeMeta represents an InfrastructureCluster.
// Infrastructure clusters are typically in groups like "infrastructure.cluster.x-k8s.io"
// and have kind names ending with "Cluster" (e.g., ProxmoxCluster, AWSCluster).
func isInfrastructureCluster(typeMeta metav1.TypeMeta) bool {
	// Check if APIVersion contains "infrastructure" and Kind ends with "Cluster"
	if len(typeMeta.APIVersion) == 0 || len(typeMeta.Kind) < 7 {
		return false
	}

	// Parse APIVersion to get group
	gv, err := schema.ParseGroupVersion(typeMeta.APIVersion)
	if err != nil {
		return false
	}

	// Check if group contains "infrastructure" and kind ends with "Cluster"
	return (gv.Group == "infrastructure.cluster.x-k8s.io" ||
		len(gv.Group) > 14 && gv.Group[:14] == "infrastructure") &&
		len(typeMeta.Kind) >= 7 && typeMeta.Kind[len(typeMeta.Kind)-7:] == "Cluster"
}

// extractClusterName extracts the cluster name from an InfrastructureCluster name.
// By convention, InfrastructureCluster names match the Cluster name or follow
// predictable patterns like "<clustername>-<suffix>".
func extractClusterName(infraClusterName string) string {
	// In most cases, the InfrastructureCluster name equals the Cluster name
	// or is a prefix. CAPI topology controller ensures this naming convention.
	// For now, we simply return the name as-is, assuming it matches.
	// More sophisticated matching could be added if needed.
	return infraClusterName
}

// ensureIPAddressClaimForBeforeCreate creates or retrieves an existing IPAddressClaim for BeforeClusterCreate hook.
// NOTE: Cannot set ownerReference because Cluster doesn't exist in etcd yet.
// The controller will adopt this claim later by setting ownerReference.
func (e *VIPExtension) ensureIPAddressClaimForBeforeCreate(ctx context.Context, cluster *clusterv1.Cluster, claimName, poolName string) (*unstructured.Unstructured, error) {
	log := e.Logger.WithValues("cluster", cluster.Name, "namespace", cluster.Namespace, "claim", claimName, "pool", poolName)

	claimGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind}
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(claimGVK)

	namespacedName := types.NamespacedName{Name: claimName, Namespace: cluster.Namespace}

	// Try to get existing claim
	err := e.Client.Get(ctx, namespacedName, claim)
	if err == nil {
		// Claim already exists
		log.Info("IPAddressClaim already exists, will use it")
		return claim, nil
	}

	if !errors.IsNotFound(err) {
		// Unexpected error
		log.Error(err, "failed to get IPAddressClaim")
		return nil, fmt.Errorf("get IPAddressClaim: %w", err)
	}

	// Create new claim
	log.Info("IPAddressClaim not found, creating new one")
	claim.SetName(claimName)
	claim.SetNamespace(cluster.Namespace)
	claim.SetLabels(map[string]string{
		roleLabel: controlPlaneRole,
		// Add cluster name label for later adoption by controller
		"cluster.x-k8s.io/cluster-name": cluster.Name,
	})

	// Set poolRef
	if err := unstructured.SetNestedField(claim.Object, map[string]interface{}{
		"apiGroup": ipamGroup,
		"kind":     globalPoolKind,
		"name":     poolName,
	}, "spec", "poolRef"); err != nil {
		return nil, fmt.Errorf("set poolRef: %w", err)
	}

	if err := e.Client.Create(ctx, claim); err != nil {
		if errors.IsAlreadyExists(err) {
			// Race condition: another process created it
			log.Info("IPAddressClaim was created by another process, fetching it")
			if err := e.Client.Get(ctx, namespacedName, claim); err != nil {
				return nil, fmt.Errorf("fetch existing IPAddressClaim: %w", err)
			}
			return claim, nil
		}
		log.Error(err, "failed to create IPAddressClaim")
		return nil, fmt.Errorf("create IPAddressClaim: %w", err)
	}

	log.Info("IPAddressClaim created successfully")
	return claim, nil
}

// waitForVIPInBeforeCreate waits for VIP allocation with longer timeout for BeforeClusterCreate hook.
func (e *VIPExtension) waitForVIPInBeforeCreate(ctx context.Context, namespace string, claim *unstructured.Unstructured) (string, error) {
	log := e.Logger.WithValues("claim", claim.GetName(), "namespace", namespace)

	claimGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind}
	namespacedName := types.NamespacedName{Name: claim.GetName(), Namespace: namespace}

	var allocatedIP string
	err := wait.PollUntilContextTimeout(ctx, beforeCreateIPInterval, beforeCreateIPTimeout, true, func(ctx context.Context) (bool, error) {
		// Refresh claim
		freshClaim := &unstructured.Unstructured{}
		freshClaim.SetGroupVersionKind(claimGVK)
		if err := e.Client.Get(ctx, namespacedName, freshClaim); err != nil {
			if errors.IsNotFound(err) {
				log.V(1).Info("IPAddressClaim not found yet, retrying")
				return false, nil // Retry
			}
			return false, err // Permanent error
		}

		// Try to get IP from claim
		ip, err := e.getIPFromClaim(ctx, namespace, freshClaim)
		if err != nil {
			log.V(1).Info("IP not ready yet, retrying", "error", err.Error())
			return false, nil // Retry
		}

		allocatedIP = ip
		log.Info("IP successfully allocated", "ip", allocatedIP)
		return true, nil // Success
	})

	if err != nil {
		if wait.Interrupted(err) {
			return "", fmt.Errorf("timeout waiting for IP allocation after %v: %w", beforeCreateIPTimeout, err)
		}
		return "", fmt.Errorf("error waiting for IP allocation: %w", err)
	}

	return allocatedIP, nil
}
