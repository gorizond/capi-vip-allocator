# Changelog

All notable changes to this project will be documented in this file.

## [v0.1.8] - 2025-10-19

### Fixed

- 🐛 **Critical bug**: Controller now supports namespace-scoped ClusterClass resources
  - Previously: Controller tried to read ClusterClass as cluster-scoped only
  - Now: Controller first tries cluster-scoped, then falls back to namespace-scoped
  - **Impact**: Fixes issue where ClusterClass in namespace (e.g., `clusters-proxmox`) couldn't be read
  - Error was: `cannot get ClusterClass "rke2-proxmox-class"` - NotFound error
  - Result: Cluster stuck in Pending state, `spec.controlPlaneEndpoint.host` never set

### Changed

- Updated `getClusterClass()` function with namespace fallback logic
  - First attempt: cluster-scoped (no namespace)
  - Second attempt: namespace-scoped (using cluster's namespace)
  - Better error messages showing both attempts
- Updated `patchClusterEndpoint()` signature to accept `clusterNamespace` parameter
- Updated all callers and tests to pass namespace parameter

### Testing

- Added test `TestGetClusterClass_NamespaceScoped` - verifies namespace-scoped ClusterClass can be read
- Added test `TestGetClusterClass_ClusterScoped` - verifies cluster-scoped ClusterClass still works
- All existing tests pass with updated function signatures

### Impact

- ✅ Fixes cluster creation when ClusterClass is in a namespace
- ✅ Maintains backward compatibility with cluster-scoped ClusterClass
- ✅ RBAC already has correct permissions (ClusterRole can read across namespaces)

---

## [v0.1.6] - 2025-10-19

### Fixed

- 🐛 **Critical bug**: Controller now properly detects ClusterClass mode (direct vs legacy)
  - Controller checks if ClusterClass defines `clusterVip` variable before patching
  - **Direct mode** (no `clusterVip` variable): Only patches `spec.controlPlaneEndpoint.host`
  - **Legacy mode** (has `clusterVip` variable): Patches both endpoint AND variable
  - Previously: Controller ALWAYS tried to add `clusterVip` variable, causing webhook rejection
- 🔒 **RBAC permissions**: Added missing permissions for `clusterclasses` resource
  - Controller can now read ClusterClass to detect variables
  - Fixes: `cannot list resource "clusterclasses" in API group "cluster.x-k8s.io"`

### Changed

- Added `getClusterClass()` function to fetch ClusterClass
- Added `hasClusterVipVariable()` function to check for variable presence
- Updated `patchClusterEndpoint()` with conditional logic based on ClusterClass
- Updated RBAC with `clusterclasses` read permissions (get, list, watch)

### Testing

- Added test `TestClusterReconciler_Reconcile_AssignsIPAddress_DirectMode` - verifies no variable added
- Added test `TestClusterReconciler_Reconcile_AssignsIPAddress_LegacyMode` - verifies variable added
- Fixed test helpers to use correct API versions (v1alpha2 for pools, v1beta1 for claims)

### Impact

- ✅ Fixes cluster creation errors: `variable is not defined`
- ✅ Allows ClusterClass to work WITHOUT `clusterVip` variable
- ✅ Maintains backward compatibility with legacy ClusterClass

---

## [v0.1.0] - 2025-10-18

### Major: Runtime Extension Support

**Race condition fully resolved via CAPI Runtime Extensions.**

#### Added

- Runtime Extension server with HTTP handlers for CAPI hooks
  - `GeneratePatches` hook - synchronous VIP allocation BEFORE Cluster creation
  - `BeforeClusterCreate`, `BeforeClusterDelete`, `AfterClusterUpgrade` hooks
  - HTTP server on port 9443 with TLS support
  - Discovery endpoint for CAPI registration

- Kubernetes resources
  - `ExtensionConfig` - registers extension with CAPI
  - `Service` - for Runtime Extension server
  - `Certificate` + `Issuer` - TLS via cert-manager

- Command-line flags
  - `--enable-runtime-extension` (default: false)
  - `--runtime-extension-port` (default: 9443)

- Documentation and examples in `examples/` directory

#### Changed

- Reconciler now updates `clusterVip` variable in `topology.variables`
- Deployment includes Runtime Extension port and TLS volume
- Manager starts Runtime Extension server when flag enabled

#### Fixed

- ✅ **Race condition eliminated**: VIP allocated in `GeneratePatches` hook
- ✅ InfrastructureCluster created without `controlPlaneEndpoint.host required` error
- ✅ Cluster transitions to `Provisioning` immediately (no retry needed)

#### Requirements

- CAPI v1.5+ with `RuntimeSDK=true` feature gate
- cert-manager for TLS certificates

---

## [v0.0.6] - 2025-10-18

### Changed

- Reconciler patches `clusterVip` variable in `spec.topology.variables`
- Support for both manual and automatic VIP allocation

### Known Issues

- ⚠️ **Race condition**: CAPI Topology Controller creates InfrastructureCluster before Cluster update
- Requires ClusterClass patching to work properly

---

## [v0.0.5] - 2025-10-18

### Fixed

- IPAddressClaim `poolRef` structure: uses `apiGroup` instead of `apiVersion`
- Admission webhook now accepts claim creation

### Changed

- `poolRef` now uses separate `apiGroup: "ipam.cluster.x-k8s.io"` field

---

## [v0.0.4] - 2025-10-18

### Fixed

- API version for IPAddressClaim: `v1alpha2` → `v1beta1`

### Known Issues

- ❌ Validation webhook rejects: `poolRef.apiGroup: Invalid value: "null"`

---

## [v0.0.3] - 2025-10-17

### Added

- Initial operator release
- Reconciler for Cluster resources
- CAPI IPAM integration
  - `GlobalInClusterIPPool` - discovery by labels
  - `IPAddressClaim` - creation with ownerReferences
  - `IPAddress` - wait for allocation
- RBAC manifests
- Deployment, Service, ServiceAccount

### Known Issues

- ❌ Uses non-existent `v1alpha2` version for IPAddressClaim
- ❌ Error: `no matches for kind "IPAddressClaim" in version "ipam.cluster.x-k8s.io/v1alpha2"`

---

## Upgrade Guide

### v0.0.x → v0.1.0

#### Step 1: Enable RuntimeSDK in CAPI

```bash
kubectl patch deployment capi-controller-manager -n capi-system --type=json -p '[
  {"op": "replace", "path": "/spec/template/spec/containers/0/args/2", 
   "value": "--feature-gates=RuntimeSDK=true,ClusterTopology=true,MachinePool=true"}
]'
```

#### Step 2: Update operator

```bash
kubectl edit capiprovider capi-vip-allocator -n capi-system
# Change spec.version to v0.1.0
```

#### Step 3: Verify

```bash
kubectl get extensionconfig vip-allocator -n capi-system
kubectl logs -n capi-system -l control-plane=capi-vip-allocator-controller-manager
```

### Rollback: v0.1.0 → v0.0.6

```bash
# Disable Runtime Extension
kubectl patch deployment capi-vip-allocator-controller-manager -n capi-system --type=json -p '[
  {"op": "replace", "path": "/spec/template/spec/containers/0/args/1", 
   "value": "--enable-runtime-extension=false"}
]'

# Rollback version
kubectl edit capiprovider capi-vip-allocator -n capi-system
# Change spec.version to v0.0.6
```

---

## Version Matrix

| Version | Mode | Race Condition | RuntimeSDK | Direct Mode Bug | Status |
|---------|------|----------------|------------|-----------------|--------|
| v0.0.3 | Reconciler | ❌ | No | N/A | Deprecated |
| v0.0.4 | Reconciler | ❌ | No | N/A | Deprecated |
| v0.0.5 | Reconciler | ❌ | No | N/A | Deprecated |
| v0.0.6 | Reconciler | ⚠️ | No | N/A | Deprecated |
| v0.1.0 | Runtime Extension | ✅ Fixed | Yes | ❌ | Deprecated |
| **v0.1.6** | **Runtime Extension** | **✅ Fixed** | **Yes** | **✅ Fixed** | **Recommended** |

---

[v0.1.6]: https://github.com/gorizond/capi-vip-allocator/releases/tag/v0.1.6
[v0.1.0]: https://github.com/gorizond/capi-vip-allocator/releases/tag/v0.1.0
[v0.0.6]: https://github.com/gorizond/capi-vip-allocator/releases/tag/v0.0.6
[v0.0.5]: https://github.com/gorizond/capi-vip-allocator/releases/tag/v0.0.5
[v0.0.4]: https://github.com/gorizond/capi-vip-allocator/releases/tag/v0.0.4
[v0.0.3]: https://github.com/gorizond/capi-vip-allocator/releases/tag/v0.0.3
