# Changelog

All notable changes to this project will be documented in this file.

## [v0.7.0] - 2025-10-20

### 🚀 New Feature: Prometheus Metrics

**Comprehensive observability for VIP allocation and IP pool management!**

### Added

- **Prometheus Metrics** - Full observability of VIP allocator operations
  - **Allocation metrics**: Track successful allocations, errors, and duration
    - `capi_vip_allocator_allocations_total` (counter) - successful VIP allocations by role/cluster_class
    - `capi_vip_allocator_allocation_errors_total` (counter) - allocation errors by reason
    - `capi_vip_allocator_allocation_duration_seconds` (histogram) - allocation latency
  
  - **Pool metrics**: Monitor IP pool capacity and utilization
    - `capi_vip_allocator_pools_available` (gauge) - available pools
    - `capi_vip_allocator_pool_addresses_total` (gauge) - total IPs in pool
    - `capi_vip_allocator_pool_addresses_free` (gauge) - free IPs
    - `capi_vip_allocator_pool_addresses_used` (gauge) - used IPs
  
  - **Claim metrics**: Track IPAddressClaim lifecycle
    - `capi_vip_allocator_claims_total` (gauge) - active claims
    - `capi_vip_allocator_claims_ready` (gauge) - claims with allocated IP
    - `capi_vip_allocator_claims_pending` (gauge) - claims waiting for IP
  
  - **Reconcile metrics**: Monitor controller operations
    - `capi_vip_allocator_reconcile_total` (counter) - reconcile operations by result
    - `capi_vip_allocator_reconcile_duration_seconds` (histogram) - reconcile latency

- **Metrics endpoint**: `:8080/metrics` (default, configurable via `--metrics-bind-address`)

- **Documentation**:
  - Full metrics reference in README
  - Example PromQL queries for common use cases
  - ServiceMonitor example for Prometheus Operator
  - Grafana dashboard recommendations

### Integration Example

```yaml
# ServiceMonitor for Prometheus Operator
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: capi-vip-allocator
  namespace: capi-system
spec:
  selector:
    matchLabels:
      control-plane: capi-vip-allocator-controller-manager
  endpoints:
    - port: metrics
      interval: 30s
```

### Useful Queries

```promql
# Allocation success rate
rate(capi_vip_allocator_allocations_total[5m])

# Pool utilization %
(capi_vip_allocator_pool_addresses_used / capi_vip_allocator_pool_addresses_total) * 100

# P95 allocation latency
histogram_quantile(0.95, rate(capi_vip_allocator_allocation_duration_seconds_bucket[5m]))

# Pending claims (alert when > 5)
sum(capi_vip_allocator_claims_pending)
```

---

## [v0.6.5] - 2025-10-20

### Added

- **Comma-separated values support in GlobalInClusterIPPool labels**
  - `vip.capi.gorizond.io/cluster-class` now supports multiple cluster classes: `"class1,class2,class3"`
  - `vip.capi.gorizond.io/role` now supports multiple roles: `"control-plane,ingress"`
  - Allows sharing single IP pool across multiple cluster classes
  - Allows sharing single IP pool for both control-plane and ingress VIPs
  - Example: `cluster-class: "rke2-proxmox,rke2-vsphere,k3s-proxmox"`
  - Example: `role: "control-plane,ingress"`

### Changed

- Pool matching logic now checks comma-separated values (backward compatible)
- Updated documentation with examples of shared pools
- Added unit tests for comma-separated label matching

---

## [v0.6.1] - 2025-10-20

### Changed

- **Ingress VIP stored in annotation** (not variable!)
  - Writes to `metadata.annotations["vip.capi.gorizond.io/ingress-vip"]`
  - Simpler: no need to define `ingressVip` variable in ClusterClass
  - Fleet GitRepo reads from `${ .ClusterValues.Cluster.metadata.annotations["vip.capi.gorizond.io/ingress-vip"] }`

**Migration from v0.6.0:**
- Remove `ingressVip` variable from ClusterClass.spec.variables
- Update kube-vip-ingress config to read from annotation (not variable)

---

## [v0.6.0] - 2025-10-20

### 🚀 New Feature: Ingress VIP Support

**Automatic allocation of dedicated VIP for Ingress/LoadBalancer nodes!**

Ingress VIP is allocated **BY DEFAULT** for all clusters (if ClusterClass defines `ingressVip` variable).

To **disable** ingress VIP allocation, add annotation:

```yaml
metadata:
  annotations:
    vip.capi.gorizond.io/ingress-enabled: "false"  # Disable ingress VIP
```

### Added

- **Ingress VIP Allocation** - Automatic allocation of separate VIP for ingress/loadbalancer nodes
  - **Enabled by default** if ClusterClass defines `ingressVip` variable
  - Can be **disabled** via annotation `vip.capi.gorizond.io/ingress-enabled: "false"`
  - Creates separate IPAddressClaim with `role: ingress` label
  - Writes VIP to `Cluster.spec.topology.variables[ingressVip]`
  - Uses separate GlobalInClusterIPPool (with `role: ingress` label)

- **Custom Variable `ingressVip`** - Available in ClusterClass patches
  - Similar to `clusterVip` but for ingress nodes
  - Can be used in kube-vip configuration for loadbalancer workers
  - Automatically added when annotation is present

### Integration Example

```yaml
# 1. Create Ingress IP Pool
apiVersion: ipam.cluster.x-k8s.io/v1alpha2
kind: GlobalInClusterIPPool
metadata:
  name: ingress-vip-pool
  labels:
    vip.capi.gorizond.io/cluster-class: my-cluster-class
    vip.capi.gorizond.io/role: ingress  # ← ingress role
spec:
  addresses:
    - "10.0.0.100-10.0.0.110"

# 2. Create Cluster (ingress VIP enabled by default)
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
  # No annotation needed! Ingress VIP allocated by default
  # To disable: vip.capi.gorizond.io/ingress-enabled: "false"
spec:
  topology:
    class: my-cluster-class

# 3. Result (automatic after 5-10 seconds):
# spec:
#   topology:
#     variables:
#       - name: clusterVip
#         value: "10.0.0.15"  # Control plane VIP
#       - name: ingressVip
#         value: "10.0.0.101"  # Ingress VIP ✨
```

See [Ingress VIP Integration](#ingress-vip-integration) in README for full configuration.

---

## [v0.5.0] - 2025-10-20

### 🚀 Major: Back to Reconcile Controller Architecture

**Critical Discovery**: External patches in ClusterClass **CANNOT** patch Cluster object!

**External Patches Limitation** (from CAPI docs):
```
"Like inline patches, external patches are only allowed to change fields in spec.template.spec"
```

External patches work with **Templates** (ProxmoxClusterTemplate), NOT actual objects (Cluster)!

**Testing Evidence (session 033, cluster rke2-proxmox-test-g15)**:
- ✅ External patch registered in ClusterClass correctly
- ✅ GeneratePatches hook CALLED by topology controller
- ❌ GeneratePatches receives TEMPLATES, not Cluster objects
- ❌ Cannot patch Cluster.spec.controlPlaneEndpoint (only Template.spec.template.spec)

**Architecture Decision**: Return to v0.2.x reconcile controller approach

### Changed

- **Reconcile Controller** - RE-ENABLED (default: true)
  - Watches Cluster objects with ClusterTopology
  - Allocates VIP from GlobalInClusterIPPool via IPAM
  - Sets Cluster.spec.controlPlaneEndpoint.host
  - ClusterClass inline patches use `{{ .builtin.cluster.spec.controlPlaneEndpoint.host }}`
  - Topology controller waits until VIP is set before applying patches
  
- **Runtime Extension** - DISABLED (default: false)
  - External patches cannot solve the VIP allocation problem
  - Kept for backward compatibility and future hooks (AfterUpgrade, BeforeDelete)

### Migration from v0.4.x

1. Remove external patch from ClusterClass:
   ```yaml
   spec:
     patches:
       - name: vip-allocator  # DELETE THIS
         external:
           generateExtension: vip-allocator
   ```

2. Update CAPIProvider to v0.5.0

3. Reconcile controller will handle VIP allocation automatically

---

## [v0.4.1] - 2025-10-20

### Fixed

- Removed handleBeforeClusterCreate method from server.go (compilation fix)
- GitHub Actions build failure: "s.extension.BeforeClusterCreate undefined"
- v0.4.0 removed BeforeClusterCreate from extension.go but forgot to remove handler method

---

## [v0.4.0] - 2025-10-20

### 🚀 Major: Correct Architecture - GeneratePatches is the ONLY hook

**Critical Discovery (session 033)**: BeforeClusterCreate hook **CANNOT** modify Cluster object!

**Root Cause**:
```
v0.2.0-v0.3.2 Architecture (BROKEN):
- BeforeClusterCreate sets request.Cluster.Spec.ControlPlaneEndpoint.Host = "10.2.0.20"
- CAPI ignores these changes
- Cluster saved to etcd WITHOUT VIP (host="")
- Topology reconcile fails with validation error
❌ Result: ProxmoxCluster validation error - "provided endpoint address is not a valid IP or FQDN"
```

**Testing Evidence (session 033, cluster rke2-proxmox-test-g13)**:
- ✅ BeforeClusterCreate hook executed successfully
- ✅ Logs: "VIP set in BeforeClusterCreate hook - cluster will be created with this endpoint vip=10.2.0.20"
- ❌ Cluster in etcd: `spec.controlPlaneEndpoint.host = ""` (EMPTY!)
- ❌ ProxmoxCluster validation failed

**Conclusion**: BeforeClusterCreate hook is **read-only** for Cluster object. Modifications to `request.Cluster` are silently ignored by CAPI.

### Changed

#### Architecture (back to v0.1.9 approach + reconciler disabled)

- **GeneratePatches Hook** - THE ONLY source of VIP allocation and patching
  - Allocates VIP from GlobalInClusterIPPool
  - Creates IPAddressClaim (without ownerReference - Cluster not in etcd yet)
  - Waits for IPAM allocation (max 25s with retry)
  - Returns patch response with VIP for both Cluster AND InfrastructureCluster
  - Timeout: 30s (CAPI maximum)
  
- **Reconciler Controller** - DISABLED by default (opt-in via `--enable-reconciler`)
  - Prevents async race condition with GeneratePatches
  - Only for backward compatibility
  
- **BeforeClusterCreate Hook** - REMOVED
  - Cannot modify Cluster object (CAPI design limitation)
  - Was source of confusion in v0.2.0-v0.3.x

#### Removed

- BeforeClusterCreate hook and all related code
- BeforeClusterCreate handler from HTTP server
- ensureIPAddressClaimForBeforeCreate() function
- waitForVIPInBeforeCreate() function
- beforeCreateIPTimeout and beforeCreateIPInterval constants

#### Fixed

- metadata.yaml: minor version 3 → 4

### Migration from v0.3.x

**Automatic** - no action required:
- GeneratePatches hook already existed in v0.3.x
- Only BeforeClusterCreate removed (was not working anyway)
- Reconciler already disabled by default
- No changes to ClusterClass integration

### Performance

- **GeneratePatches**: 5-15 seconds (IPAM allocation + patching)
- **Total overhead**: Same as v0.1.9 (working version)
- **No race condition**: Reconciler disabled

### Why v0.4.0 is correct

|

 Feature | v0.1.9 | v0.2.0-v0.3.x | v0.4.0 |
|---------|--------|----------------|---------|
| VIP allocation | GeneratePatches | BeforeClusterCreate | GeneratePatches ✅ |
| Can modify Cluster? | ✅ Yes (patch) | ❌ No (ignored) | ✅ Yes (patch) |
| Reconciler | ✅ Enabled | ❌ Enabled (race) | ✅ Disabled |
| Working? | ✅ Yes | ❌ No | ✅ Yes |

**Lesson learned**: CAPI Runtime SDK hooks that modify objects MUST use patch responses, not direct object modification.

---

## [v0.3.2] - 2025-10-20

### Fixed

- 🔴 **Critical bug**: Fixed BeforeClusterCreate hook timeout validation error
  - **Problem**: CAPI Runtime SDK validation requires hook timeouts to be ≤ 30 seconds
  - **v0.3.0/v0.3.1**: Set BeforeClusterCreate timeout to 60s → **discovery failed**
  - **v0.3.2**: Reduced to 30s (CAPI maximum allowed)
  - **Error message**: `"handler vip-allocator-before-create timeoutSeconds 60 must be between 0 and 30"`
  
- Adjusted VIP allocation wait timeout accordingly:
  - `beforeCreateIPTimeout`: 55s → 25s (must be < hook timeout of 30s)
  - Still sufficient for IPAM to allocate IP from pool
  
### Technical Details

**CAPI Runtime SDK Constraints**:
- All hook timeouts must be between 0 and 30 seconds (enforced by validation)
- Exceeding this limit causes ExtensionConfig discovery failure
- Extension handlers not registered until validation passes

**Impact of v0.3.0/v0.3.1 Bug**:
- ExtensionConfig status: `Discovered: False`
- BeforeClusterCreate hook: NOT registered with CAPI
- VIP allocation: NOT working (hook never called)
- Result: Clusters created without VIP, validation failures

**v0.3.2 Fix**:
- BeforeClusterCreate timeout: 60s → 30s ✅
- IP allocation wait: 55s → 25s ✅
- ExtensionConfig validation: passes ✅
- Hooks: properly registered ✅

---

## [v0.3.1] - 2025-10-20

### Fixed

- Updated metadata.yaml properly (minor: 3 for v0.3.x series)
- No code changes, only release metadata correction

---

## [v0.3.0] - 2025-10-20

### 🚀 Major: Eliminated Race Condition - BeforeClusterCreate + GeneratePatches Architecture

**Problem in v0.2.x**: Reconcile controller created async race condition with CAPI topology controller.

**Root Cause Analysis (session 031)**:
```
v0.2.1 Race Condition Timeline:
T+0ms    - Cluster created with host=""
T+100ms  - CAPI topology controller applies ClusterClass patches with EMPTY host
T+500ms  - Reconciler asynchronously patches host="10.2.0.20"
T+3000ms - BeforeClusterCreate hook sees host already set, skips allocation
❌ Result: ProxmoxCluster created with EMPTY controlPlaneEndpoint (validation failed)
```

**Solution in v0.3.0**: Two-phase synchronous VIP allocation:
```
v0.3.0 Correct Timeline:
T+0ms - BeforeClusterCreate hook (SYNCHRONOUS)
  → Allocates VIP from IP pool
  → Sets request.Cluster.Spec.ControlPlaneEndpoint.Host = "10.2.0.20"
  → Returns success
T+0ms - Cluster saved to etcd WITH VIP ✅
T+100ms - CAPI topology controller starts
T+200ms - GeneratePatches hook (synchronous)
  → Reads VIP from Cluster object
  → Patches ProxmoxCluster.spec.controlPlaneEndpoint.host
  → Returns patches
T+300ms - ProxmoxCluster created WITH correct VIP ✅
```

### Changed

#### Architecture

- **BeforeClusterCreate Hook** - PRIMARY VIP allocator (synchronous, blocks Cluster creation)
  - Allocates VIP from GlobalInClusterIPPool
  - Creates IPAddressClaim (without ownerReference - Cluster not in etcd yet)
  - Waits for IPAM allocation (max 55s)
  - Sets `request.Cluster.Spec.ControlPlaneEndpoint.Host` BEFORE Cluster is persisted
  - Blocks cluster creation if IP pool not found or VIP allocation fails

- **GeneratePatches Hook** - SECONDARY patcher (synchronous, fast)
  - Extracts VIP from Cluster object (already allocated by BeforeClusterCreate)
  - Patches InfrastructureCluster (ProxmoxCluster) with VIP
  - Does NOT allocate VIPs (removed in v0.3.0)
  - Timeout reduced from 30s to 10s (no allocation, just patching)

- **Reconcile Controller** - DISABLED by default (opt-in via `--enable-reconciler=false`)
  - Creating async race condition with BeforeClusterCreate
  - Only for backward compatibility with clusters created without runtime extension
  - NOT RECOMMENDED for new deployments

#### Command-line Flags

- Added `--enable-reconciler=false` - disable reconcile controller (default: false)
- Changed `--enable-runtime-extension` default from `false` to `true`
- Runtime extension is now REQUIRED for VIP allocation

#### Deployment

- Updated default args: `--enable-reconciler=false` added
- Runtime Extension enabled by default

#### Error Handling

- BeforeClusterCreate now returns FAILURE if IP pool not found (was: skip silently)
- Stricter validation: user must configure IP pool with proper labels

### Removed

- **Reconcile loop async VIP allocation** - source of race condition in v0.2.x
- Reconciler is disabled by default, must be explicitly enabled (not recommended)

### Performance

- **BeforeClusterCreate**: 5-15 seconds (IPAM allocation time)
- **GeneratePatches**: <100ms (no allocation, just patching)
- **Total cluster creation time**: No change (but eliminates retries from validation failures)

### Migration from v0.2.x

**Automatic** - no action required:
- Existing clusters: Continue working (no change)
- New clusters: Use BeforeClusterCreate + GeneratePatches (automatic)
- Runtime Extension: Enabled by default
- Reconciler: Disabled by default (no race condition)

**Breaking Changes**: None
- All v0.2.x clusters remain functional
- ClusterClass patches remain compatible
- IPAM configuration unchanged

### Testing

Updated all tests to reflect new architecture:
- BeforeClusterCreate allocates VIP synchronously
- GeneratePatches patches InfrastructureCluster only
- Reconciler disabled by default

---

## [v0.2.0] - 2025-10-20

### 🚀 Major: Synchronous VIP Allocation in BeforeClusterCreate Hook

**Problem Solved**: CAPI topology controller was rendering infrastructure objects (ProxmoxCluster, RKE2ControlPlane) **BEFORE** the VIP was allocated, causing validation errors with empty `{{ .builtin.controlPlane.endpoint.host }}`.

**Solution**: BeforeClusterCreate hook now **synchronously** allocates VIP and sets `Cluster.Spec.ControlPlaneEndpoint.Host` **BEFORE** the cluster object is created in etcd.

### Changed

#### Runtime Extension (pkg/runtime/extension.go)

- ✅ **BeforeClusterCreate hook**: Changed from no-op to synchronous VIP allocator
  - Creates or retrieves existing IPAddressClaim
  - Waits for VIP allocation from IPAM (timeout: 55s)
  - Sets `request.Cluster.Spec.ControlPlaneEndpoint.Host` synchronously
  - Returns retry response on timeout (retry after 5s)
  - **Result**: CAPI topology controller gets correct VIP when rendering infrastructure objects
  
- 📊 **New timeout constants**:
  - `beforeCreateIPTimeout = 55s` (must be < hook timeout of 60s)
  - `beforeCreateIPInterval = 1s` (polling interval)

- 🔧 **New helper functions**:
  - `ensureIPAddressClaimForBeforeCreate()` - creates claim without ownerReference (Cluster doesn't exist yet)
  - `waitForVIPInBeforeCreate()` - waits for VIP with longer timeout

#### Runtime Extension Server (pkg/runtime/server.go)

- ⏱️ **Increased timeout**: BeforeClusterCreate hook timeout: 10s → 60s
- 🔒 **Changed FailurePolicy**: `FailurePolicyIgnore` → `FailurePolicyFail`
  - Blocks cluster creation if VIP allocation fails
  - Ensures infrastructure objects never rendered without VIP

#### Cluster Reconciler (pkg/controller/cluster_reconciler.go)

- 🔄 **Controller as fallback**: Now works ONLY as fallback mechanism
  - **Early check**: Skips reconcile if `Cluster.Spec.ControlPlaneEndpoint.Host` already set
  - Logs: `"controlPlaneEndpoint already set (by BeforeClusterCreate hook or manual configuration), skipping reconcile"`
  - **Adoption**: Still adopts IPAddressClaim (sets ownerReference) even when VIP already set
  - **Fallback mode**: Allocates VIP only for clusters created without BeforeClusterCreate hook

### Testing

- ✅ Added unit test: `TestClusterReconciler_Reconcile_SkipsWhenVIPAlreadySet`
  - Verifies controller skips clusters with VIP already set
  - Ensures no double-allocation of VIPs
- ✅ All existing tests pass with new logic

### Edge Cases Handled

| Scenario | Behavior |
|----------|----------|
| IPAddressClaim already exists | Uses existing claim, doesn't create duplicate |
| VIP not allocated within 55s | Returns failure with retry after 5s |
| GlobalInClusterIPPool not found | Skips VIP allocation (not error - might be intentional) |
| Pool exhausted (no free IPs) | Returns failure with error message |
| Cluster.Spec.ControlPlaneEndpoint.Host already set | Skips hook (returns success immediately) |
| No topology defined | Skips hook (returns success immediately) |
| Controller reconciles after hook | Skips reconcile (early return) |

### Expected Behavior

#### Before v0.2.0 (v0.1.12):
```
1. BeforeClusterCreate hook → return success (no-op)
2. CAPI topology controller starts rendering ProxmoxCluster
3. {{ .builtin.controlPlane.endpoint.host }} = "" (empty!)
4. ProxmoxCluster.spec.controlPlaneEndpoint.host = "" → validation error ❌
5. Parallel: GeneratePatches creates IPAddressClaim, waits for VIP
6. But too late - topology reconcile already failed
```

#### After v0.2.0:
```
1. BeforeClusterCreate hook:
   a. Creates IPAddressClaim
   b. Waits for VIP from IPAM (e.g., 10.2.0.20)
   c. Sets request.Cluster.Spec.ControlPlaneEndpoint.Host = "10.2.0.20"
   d. Returns success
2. Cluster object created in etcd with VIP already set
3. CAPI topology controller starts rendering ProxmoxCluster
4. {{ .builtin.controlPlane.endpoint.host }} = "10.2.0.20" ✅
5. ProxmoxCluster.spec.controlPlaneEndpoint.host = "10.2.0.20" → validation success ✅
6. RKE2ControlPlane gets VIP through builtin variable ✅
7. Controller reconciles, sees VIP already set → skips
```

### Performance Impact

- **BeforeClusterCreate hook duration**: 5-15 seconds (depends on IPAM responsiveness)
- **Cluster creation time**: Increased by hook duration, but eliminates retries from failed topology reconciles
- **Net benefit**: Faster overall cluster creation (no wasted cycles on invalid infrastructure objects)

### Backward Compatibility

- ✅ **Existing clusters**: Continue working (controller still handles VIP allocation)
- ✅ **Manual VIP configuration**: Still supported (hook skips if VIP already set)
- ✅ **Clusters without topology**: Still supported (hook skips non-topology clusters)
- ✅ **Hook disabled**: Controller works as before (fallback mode)

### Migration

No migration needed - automatic:
- New clusters: VIP allocated by BeforeClusterCreate hook
- Existing clusters: VIP managed by controller (as before)
- Both can coexist

### Known Limitations

- **IPAM latency**: If IPAM is slow (>55s), hook will timeout and retry
- **No VIP pre-allocation**: VIP allocated on-demand during cluster creation
- **Controller adoption**: IPAddressClaim initially created without ownerReference (adopted by controller after cluster creation)

---

## [v0.1.12] - 2025-10-20

### Fixed

- 🐛 **Critical bug**: Fixed HTTP handler paths to include handler names
  - CAPI Runtime SDK appends handler name to hook paths (e.g., `/beforeclustercreate/vip-allocator-before-create`)
  - Previously: handlers registered without handler name suffix → 404 errors
  - Now: handlers registered with full paths including handler names
  - **Impact**: All runtime extension hooks now work correctly (BeforeClusterCreate, GeneratePatches, etc.)

### Added

- Logging of registered handler paths on startup for debugging

### Technical Details

**Root Cause**: CAPI Runtime SDK constructs URLs by combining hook path with handler name from ExtensionConfig.
Example: `BeforeClusterCreate` hook with handler name `vip-allocator-before-create` results in URL:
```
/hooks.runtime.cluster.x-k8s.io/v1alpha1/beforeclustercreate/vip-allocator-before-create
```

**v0.1.11 (broken)**:
```go
mux.HandleFunc("/hooks.runtime.cluster.x-k8s.io/v1alpha1/beforeclustercreate", handler)
// Result: 404 when CAPI calls /beforeclustercreate/vip-allocator-before-create
```

**v0.1.12 (fixed)**:
```go
mux.HandleFunc("/hooks.runtime.cluster.x-k8s.io/v1alpha1/beforeclustercreate/vip-allocator-before-create", handler)
// Result: 200 OK
```

**Testing**: After deploying v0.1.12, logs should show:
```
registered runtime extension handlers 
  generatePatches=/hooks.runtime.cluster.x-k8s.io/v1alpha1/generatepatches/vip-allocator-generate-patches
  beforeCreate=/hooks.runtime.cluster.x-k8s.io/v1alpha1/beforeclustercreate/vip-allocator-before-create
```

And NO more 404 errors in runtime extension logs.

---

## [v0.1.11] - 2025-10-20

### Fixed

- 🐛 **Critical bug**: Fixed GeneratePatches hook to correctly patch InfrastructureCluster objects
  - Previously: Hook tried to extract cluster name from InfrastructureCluster name (unreliable)
  - Now: Uses `HolderReference.Name` from GeneratePatchesRequestItem to identify parent Cluster
  - **Impact**: ProxmoxCluster now correctly receives `controlPlaneEndpoint.host` via GeneratePatches hook
  
- 🔧 **JSON Patch fix**: Changed operation from "add" to "replace" for controlPlaneEndpoint
  - Fixes patch application for fields that may already exist in template
  - More reliable patching for InfrastructureCluster objects

- 📊 **Logging improvements**: Added comprehensive logging to Runtime Extension
  - All HTTP requests now logged with method, path, status code, and duration
  - GeneratePatches hook logs each processing step with cluster names and allocated IPs
  - InfrastructureCluster patching now logs which objects are being patched
  - Helps diagnose issues with hook invocation and patch application

- 🏥 **Health endpoint**: Added root handler for health checks
  - Returns JSON response: `{"status":"ok","service":"capi-vip-allocator-runtime-extension"}`
  - Prevents 404 errors on root path requests

### Added

- HTTP logging middleware for all Runtime Extension requests
- `responseWriterWrapper` to capture HTTP status codes in logs
- Detailed logging in `preallocateIP()` for IPAddressClaim creation flow
- Error handling for race conditions in IPAddressClaim creation
- Cluster namespace tracking in GeneratePatches for better debugging

### Changed

- Changed log level from `V(1)` (debug) to `Info` for all hook handlers
  - BeforeClusterCreate, GeneratePatches, BeforeClusterDelete, AfterClusterUpgrade, Discovery
  - Makes hook invocation visible in default logs
- Improved error messages in IPAddressClaim allocation
- Enhanced logging format with structured fields (cluster, namespace, ip, etc.)

### Technical Details

**Root Cause Analysis**:
The v0.1.10 GeneratePatches implementation had a critical bug where it couldn't reliably identify which Cluster owns an InfrastructureCluster object. It used name-based heuristics (`extractClusterName()`), which failed when:
- InfrastructureCluster name didn't exactly match Cluster name
- Multiple clusters had similar naming patterns

**Solution**:
Use `item.HolderReference.Name` from the GeneratePatchesRequest, which CAPI Runtime SDK provides specifically for this purpose. This reference points directly to the owning Cluster object.

**Testing Notes**:
After this fix, CAPI logs should show:
```
runtime-extension: incoming HTTP request method=POST path=/hooks.runtime.cluster.x-k8s.io/v1alpha1/generatepatches
GeneratePatches hook called itemsCount=N
processing cluster name=test-cluster namespace=clusters-proxmox
VIP allocated cluster=test-cluster ip=10.2.0.20 pool=galileosky-vip
found InfrastructureCluster kind=ProxmoxCluster name=test-cluster
patching InfrastructureCluster with VIP clusterName=test-cluster ip=10.2.0.20
GeneratePatches response prepared status=Success patchesCount=M
```

---

## [v0.1.9] - 2025-10-19

### Fixed

- 🐛 **Critical bug**: Runtime Extension now waits for IP allocation with retry logic
  - Previously: `GeneratePatches` hook created IPAddressClaim and immediately tried to read IP
  - Problem: IPAM controller didn't have time to allocate IP → hook returned error → topology rendered without VIP
  - Now: Added `waitForIPAllocation()` with 25s timeout and 500ms polling interval
  - **Impact**: Fixes race condition where ProxmoxCluster was created with empty `controlPlaneEndpoint`

- 🔄 **Reconciler improvements**: Added adoption logic for IPAddressClaim created by Runtime Extension
  - Runtime Extension creates claims WITHOUT ownerReference (Cluster doesn't exist in etcd yet)
  - Reconciler now detects such claims and adopts them by adding ownerReference
  - Prevents orphaned IPAddressClaim resources after cluster deletion

- 🏁 **Race condition fix**: Reconciler re-fetches Cluster after ensuring claim exists
  - Prevents reconciler from overwriting endpoint already set by Runtime Extension
  - Better logging when endpoint is already set

### Added

- `waitForIPAllocation()` function with exponential backoff retry logic
- Constants for IP allocation timeout and retry interval
- Better logging in Runtime Extension for IP allocation progress
- Adoption logic in `ensureClaim()` for claims without ownerReferences

### Changed

- `preallocateIP()` now calls `waitForIPAllocation()` instead of single-shot IP read
- Reconciler checks endpoint AFTER claim adoption (not before)
- Improved log messages with clearer context

### Technical Details

- Timeout: 25 seconds (less than CAPI hook timeout of 30s)
- Polling interval: 500ms
- Uses `wait.PollUntilContextTimeout` from `k8s.io/apimachinery/pkg/util/wait`
- Distinguishes between retryable errors (IP not ready) and permanent errors

---

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
