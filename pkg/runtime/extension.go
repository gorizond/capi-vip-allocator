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

	// IP allocation retry settings
	ipAllocationTimeout  = 25 * time.Second // Must be less than hook timeout (30s)
	ipAllocationInterval = 500 * time.Millisecond
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
// This is where we allocate the VIP and inject it as a variable.
func (e *VIPExtension) GeneratePatches(ctx context.Context, request *runtimehooksv1.GeneratePatchesRequest, response *runtimehooksv1.GeneratePatchesResponse) {
	log := e.Logger.WithName("GeneratePatches")

	log.Info("GeneratePatches hook called", "items", len(request.Items))

	// Process each item in the request
	for _, item := range request.Items {
		// Check object type
		var typeMeta metav1.TypeMeta
		if err := json.Unmarshal(item.Object.Raw, &typeMeta); err != nil {
			log.Error(err, "failed to unmarshal TypeMeta")
			continue
		}

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

		log.Info("processing cluster", "name", cluster.Name, "namespace", cluster.Namespace)

		// Skip if no topology
		if cluster.Spec.Topology == nil || cluster.Spec.Topology.Class == "" {
			log.Info("no topology, skipping", "cluster", cluster.Name)
			continue
		}

		// Skip if endpoint already set
		if cluster.Spec.ControlPlaneEndpoint.Host != "" {
			log.Info("controlPlaneEndpoint already set, skipping", "cluster", cluster.Name, "host", cluster.Spec.ControlPlaneEndpoint.Host)
			continue
		}

		// Check if clusterVip variable is already set in request variables
		existingVIP := e.getVariableValueFromList(request.Variables, "clusterVip")
		if existingVIP != "" {
			log.Info("clusterVip variable already set", "cluster", cluster.Name, "ip", existingVIP)
			// Just set it in spec
			e.addClusterPatch(response, item.UID, "/spec/controlPlaneEndpoint", map[string]interface{}{
				"host": existingVIP,
				"port": defaultPort,
			})
			continue
		}

		// Allocate IP for this cluster
		poolName, err := e.findPool(ctx, cluster.Spec.Topology.Class, controlPlaneRole)
		if err != nil {
			log.Error(err, "failed to find IP pool", "cluster", cluster.Name)
			response.SetStatus(runtimehooksv1.ResponseStatusFailure)
			response.SetMessage(fmt.Sprintf("failed to find IP pool for cluster %s: %v", cluster.Name, err))
			return
		}

		if poolName == "" {
			msg := fmt.Sprintf("no IP pool found for cluster class %q with role %q", cluster.Spec.Topology.Class, controlPlaneRole)
			log.Info(msg, "cluster", cluster.Name)
			// Don't fail - just skip this cluster
			continue
		}

		// Pre-allocate IPAddressClaim
		claimName := fmt.Sprintf("vip-cp-%s", cluster.Name)
		ip, err := e.preallocateIP(ctx, cluster, claimName, poolName)
		if err != nil {
			log.Error(err, "failed to preallocate IP", "cluster", cluster.Name)
			response.SetStatus(runtimehooksv1.ResponseStatusFailure)
			response.SetMessage(fmt.Sprintf("failed to allocate IP for cluster %s: %v", cluster.Name, err))
			return
		}

		log.Info("VIP allocated", "cluster", cluster.Name, "ip", ip, "pool", poolName)

		// Add patch to set controlPlaneEndpoint
		e.addClusterPatch(response, item.UID, "/spec/controlPlaneEndpoint", map[string]interface{}{
			"host": ip,
			"port": defaultPort,
		})

		// Add clusterVip variable to topology
		e.addClusterVariablePatch(response, item.UID, cluster, "clusterVip", ip)
	}

	response.SetStatus(runtimehooksv1.ResponseStatusSuccess)
}

// BeforeClusterCreate is called before a Cluster is created (for cleanup/validation only).
func (e *VIPExtension) BeforeClusterCreate(ctx context.Context, request *runtimehooksv1.BeforeClusterCreateRequest, response *runtimehooksv1.BeforeClusterCreateResponse) {
	log := e.Logger.WithValues("cluster", types.NamespacedName{
		Name:      request.Cluster.Name,
		Namespace: request.Cluster.Namespace,
	})

	log.Info("BeforeClusterCreate hook called (no-op)")
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
	log := e.Logger.WithValues("cluster", cluster.Name, "claim", claimName, "pool", poolName)

	// Check if claim already exists
	claimGVK := schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind}
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(claimGVK)

	namespacedName := types.NamespacedName{Name: claimName, Namespace: cluster.Namespace}

	// Try to get existing claim
	err := e.Client.Get(ctx, namespacedName, claim)
	if err == nil {
		// Claim exists, check if IP is ready
		log.V(1).Info("IPAddressClaim already exists, checking for IP")
		return e.waitForIPAllocation(ctx, cluster.Namespace, namespacedName, claim)
	}

	// Create new claim (without ownerReference - Cluster doesn't exist in etcd yet!)
	log.Info("Creating new IPAddressClaim")
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
		return "", fmt.Errorf("create IPAddressClaim: %w", err)
	}

	log.Info("IPAddressClaim created, waiting for IP allocation")
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
