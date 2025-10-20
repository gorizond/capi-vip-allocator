# CAPI VIP Allocator

Automatic Virtual IP allocation for Cluster API clusters using Runtime Extensions.

## Features

- **Zero race condition** - VIP allocated synchronously in BeforeClusterCreate hook (v0.3.0+)
- **Single source of truth** - BeforeClusterCreate is the ONLY VIP allocation mechanism
- **No reconcile loop** - Eliminates async race condition with topology controller
- **Automatic allocation** - IP from IPAM pool based on ClusterClass labels
- **Automatic cleanup** - IP released when Cluster is deleted via ownerReferences
- **CAPI native** - Uses Runtime Extensions API
- **Production ready** - TLS, health checks, strict validation

## How it works (v0.3.0+)

**Two-phase synchronous VIP allocation:**

```
Phase 1: BeforeClusterCreate Hook (PRIMARY - VIP Allocation)
Client creates Cluster (host: "")
         ↓
CAPI calls BeforeClusterCreate hook SYNCHRONOUSLY
         ↓
VIP Allocator Extension
  1. Finds IP pool by cluster-class label
  2. Creates IPAddressClaim (without ownerRef - Cluster not in etcd yet)
  3. Waits for IPAM to allocate IP (retry every 1s, max 55s)
  4. Sets request.Cluster.Spec.ControlPlaneEndpoint.Host = "10.2.0.20"
         ↓
Cluster saved to etcd WITH IP ✅

Phase 2: GeneratePatches Hook (SECONDARY - Infrastructure Patching)
CAPI Topology Controller starts rendering
         ↓
CAPI calls GeneratePatches hook SYNCHRONOUSLY
         ↓
VIP Allocator Extension
  1. Reads VIP from Cluster.Spec.ControlPlaneEndpoint.Host
  2. Patches ProxmoxCluster.spec.controlPlaneEndpoint.host = "10.2.0.20"
         ↓
ProxmoxCluster created WITH correct VIP ✅
         ↓
ClusterClass patches use {{ .builtin.controlPlane.endpoint.host }} for RKE2ControlPlane ✅
         ↓
Cleanup on delete: ownerReference ensures IPAddressClaim is deleted ✅
```

**Key difference from v0.2.x:** 
- ✅ BeforeClusterCreate allocates VIP BEFORE Cluster is saved to etcd
- ✅ GeneratePatches patches InfrastructureCluster with VIP from Cluster
- ❌ No reconcile loop! (was source of race condition in v0.2.x)

## Quick Start

### Prerequisites

1. **CAPI with RuntimeSDK enabled**

```bash
kubectl patch deployment capi-controller-manager -n capi-system --type=json -p '[
  {"op": "replace", "path": "/spec/template/spec/containers/0/args/2", 
   "value": "--feature-gates=RuntimeSDK=true,ClusterTopology=true,MachinePool=true"}
]'
```

2. **cert-manager installed**

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.0/cert-manager.yaml
```

3. **IPAM provider** (in-cluster or other)

### Installation

Using CAPIProvider (Rancher Turtles):

```yaml
apiVersion: turtles-capi.cattle.io/v1alpha1
kind: CAPIProvider
metadata:
  name: capi-vip-allocator
  namespace: capi-system
spec:
  type: addon  # Runtime Extension providers use 'addon' type
  version: v0.3.0
  fetchConfig:
    url: https://github.com/gorizond/capi-vip-allocator/releases/download/v0.3.0/capi-vip-allocator.yaml
```

**Note:** Runtime Extensions are registered via `ExtensionConfig` resource (included in the manifest), not through CAPIProvider type.

Or directly:

```bash
kubectl apply -f https://github.com/gorizond/capi-vip-allocator/releases/download/v0.3.0/capi-vip-allocator.yaml
```

### Create IP Pool

```yaml
apiVersion: ipam.cluster.x-k8s.io/v1alpha2
kind: GlobalInClusterIPPool
metadata:
  name: control-plane-vip-pool
  labels:
    vip.capi.gorizond.io/cluster-class: my-cluster-class
    vip.capi.gorizond.io/role: control-plane
spec:
  addresses:
    - "10.0.0.10-10.0.0.20"
  gateway: "10.0.0.1"
  prefix: 24
```

### Create Cluster

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
spec:
  topology:
    class: my-cluster-class
    version: v1.31.0
    controlPlane:
      replicas: 3
    workers:
      machineDeployments:
        - class: default-worker
          name: workers
          replicas: 2
```

**Done!** The `controlPlaneEndpoint.host` will be automatically allocated.

## Verification

```bash
# Check IP allocation (should appear in ~5-10 seconds)
kubectl get cluster my-cluster -o jsonpath='{.spec.controlPlaneEndpoint.host}'

# Check ExtensionConfig status
kubectl get extensionconfig vip-allocator -n capi-system

# Check operator logs
kubectl logs -n capi-system -l control-plane=capi-vip-allocator-controller-manager
```

## Configuration

### IP Pool Selection

The operator finds pools using labels:

- `vip.capi.gorizond.io/cluster-class: <clusterClassName>`
- `vip.capi.gorizond.io/role: control-plane`

### Manual VIP Override

Specify VIP manually to skip automatic allocation:

```yaml
spec:
  controlPlaneEndpoint:
    host: "10.0.0.100"
    port: 6443
```

### Runtime Extension Options

Deployment args (v0.3.0+):

- `--enable-runtime-extension=true` - Enable Runtime Extension mode (default: true, REQUIRED)
- `--enable-reconciler=false` - Enable reconcile controller (default: false, NOT RECOMMENDED - causes race condition)
- `--runtime-extension-port=9443` - Runtime Extension server port
- `--runtime-extension-name=vip-allocator` - Name of the runtime extension handler (must not contain dots, default: vip-allocator)
- `--leader-elect` - Enable leader election
- `--default-port=6443` - Default control plane port

**Important:** In v0.3.0+, the reconcile controller is disabled by default to prevent race conditions. All VIP allocation is done synchronously in BeforeClusterCreate hook.

## ClusterClass Integration

### Important: Avoid Race Conditions

The operator uses **GeneratePatches hook** to populate VIP before infrastructure objects are created. This means you should **NOT** copy `Cluster.spec.controlPlaneEndpoint.host` in your ClusterClass patches.

#### ❌ Incorrect Pattern (causes race condition)

```yaml
# DO NOT USE - this copies empty value before VIP is allocated
patches:
  - name: set-vip-on-infrastructure-cluster
    definitions:
      - jsonPatches:
          - op: replace
            path: /spec/template/spec/controlPlaneEndpoint/host
            valueFrom:
              template: "{{ .builtin.cluster.spec.controlPlaneEndpoint.host }}"
        selector:
          apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
          kind: ProxmoxClusterTemplate  # or any InfrastructureClusterTemplate
          matchResources:
            infrastructureCluster: true
```

**Why this fails:**
1. ClusterClass patches run **before** GeneratePatches hook
2. At that moment `Cluster.spec.controlPlaneEndpoint.host` is still empty
3. Infrastructure webhook rejects empty endpoint
4. Cluster creation fails with validation error

#### ✅ Correct Pattern

Use `.builtin.controlPlane.endpoint.host` instead - this value is already populated by the operator:

```yaml
# CORRECT - uses controlPlane.endpoint which is populated by operator
patches:
  - name: set-control-plane-registration
    definitions:
      - jsonPatches:
          - op: replace
            path: /spec/template/spec/registrationAddress
            valueFrom:
              template: "{{ .builtin.controlPlane.endpoint.host }}"
        selector:
          apiVersion: controlplane.cluster.x-k8s.io/v1beta1
          kind: RKE2ControlPlaneTemplate

  - name: set-control-plane-tls-san
    definitions:
      - jsonPatches:
          - op: add
            path: /spec/template/spec/tlsSAN/-
            valueFrom:
              template: "{{ .builtin.controlPlane.endpoint.host }}"
        selector:
          apiVersion: controlplane.cluster.x-k8s.io/v1beta1
          kind: RKE2ControlPlaneTemplate
```

### Hook Architecture (v0.3.0+)

The operator uses two synchronous hooks in sequence:

#### 1. BeforeClusterCreate Hook (Phase 1 - VIP Allocation)

1. **Executes BEFORE** Cluster is written to etcd (synchronous, blocking)
2. **Allocates VIP** from IP pool (with retry, max 55s)
3. **Creates IPAddressClaim** (without ownerReference - Cluster not in etcd yet)
4. **Waits for IPAM** to allocate IP address
5. **Sets VIP directly** in request object:
   - `request.Cluster.Spec.ControlPlaneEndpoint.Host = "10.2.0.20"`
6. **Returns success** to CAPI controller
7. **Cluster created** in etcd with VIP already set ✅

#### 2. GeneratePatches Hook (Phase 2 - Infrastructure Patching)

1. **Executes AFTER** Cluster exists in etcd (synchronous, fast)
2. **Reads VIP** from Cluster.Spec.ControlPlaneEndpoint.Host
3. **Patches InfrastructureCluster** (e.g., ProxmoxCluster):
   - `ProxmoxCluster.spec.controlPlaneEndpoint.host = "10.2.0.20"`
4. **Returns patches** to CAPI controller
5. **InfrastructureCluster created** with correct VIP ✅
6. **Topology controller** renders other objects with `{{ .builtin.controlPlane.endpoint.host }}`

**Why v0.3.0 is better than v0.2.x:**
- ✅ **Two-phase synchronous** - BeforeClusterCreate allocates, GeneratePatches patches
- ✅ **No reconcile loop** - eliminated async race condition
- ✅ **InfrastructureCluster support** - GeneratePatches patches ProxmoxCluster correctly
- ❌ **v0.2.x problem** - reconciler ran asynchronously, topology controller used empty VIP

### InfrastructureClusterTemplate Configuration

Your infrastructure cluster template should have **empty** host:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxClusterTemplate  # or your provider
metadata:
  name: my-cluster-template
spec:
  template:
    spec:
      controlPlaneEndpoint:
        host: ""    # ✅ Empty - will be filled by operator's GeneratePatches hook
        port: 6443  # ✅ Port can be static
```

The operator will automatically populate the `host` field for both Cluster and InfrastructureCluster objects.

## Troubleshooting

### ExtensionConfig not found

```
Error: no matches for kind "ExtensionConfig"
```

**Solution:** Enable RuntimeSDK in CAPI controller (see Prerequisites).

### VIP not allocated

Check:

1. **RuntimeSDK enabled:**
   ```bash
   kubectl logs -n capi-system -l control-plane=controller-manager | grep "Runtime SDK"
   ```

2. **IP pool exists:**
   ```bash
   kubectl get globalinclusterippool -l vip.capi.gorizond.io/cluster-class=YOUR_CLASS
   ```

3. **Pool has free IPs:**
   ```bash
   kubectl get globalinclusterippool POOL_NAME -o jsonpath='{.status.ipAddresses}'
   ```

4. **Extension logs:**
   ```bash
   kubectl logs -n capi-system -l control-plane=capi-vip-allocator-controller-manager -f
   ```

### Certificate not ready

```bash
kubectl describe certificate capi-vip-allocator-runtime-extension-cert -n capi-system
```

Check cert-manager:
```bash
kubectl get pods -n cert-manager
```

### InfrastructureCluster validation error

```
Error: failed to create ProxmoxCluster.infrastructure.cluster.x-k8s.io: 
FieldValueInvalid: spec.controlplaneEndpoint: 
Invalid value: "<no value>": provided endpoint address is not a valid IP or FQDN
```

**Причина:** Race condition - ClusterClass патч пытается скопировать `Cluster.spec.controlPlaneEndpoint.host` до того, как оператор выделит VIP.

**Решение:**

1. **Удалите конфликтующий патч** из ClusterClass:
   ```yaml
   # Удалить патч, который копирует .builtin.cluster.spec.controlPlaneEndpoint.host
   # в InfrastructureCluster
   ```

2. **Используйте правильный синтаксис** для других патчей:
   ```yaml
   # ✅ Правильно - использует .builtin.controlPlane.endpoint
   valueFrom:
     template: "{{ .builtin.controlPlane.endpoint.host }}"
   
   # ❌ Неправильно - использует .builtin.cluster.spec
   valueFrom:
     template: "{{ .builtin.cluster.spec.controlPlaneEndpoint.host }}"
   ```

3. **Проверьте логи** оператора:
   ```bash
   kubectl logs -n capi-system -l control-plane=capi-vip-allocator-controller-manager -f
   ```

См. раздел [ClusterClass Integration](#clusterclass-integration) для подробностей.

## Architecture (v0.3.0+)

### Components

- **Runtime Extension Server** - HTTP server (port 9443) handling CAPI hooks
- **BeforeClusterCreate Hook** - Allocates VIP synchronously BEFORE Cluster creation (ONLY source)
- **IPAM Integration** - Creates/manages IPAddressClaim resources
- **ownerReferences** - Automatic cleanup when Cluster is deleted

### Removed in v0.3.0

- **GeneratePatches Hook** - Removed to prevent race condition (ran after topology controller)
- **Reconciler Controller** - Disabled by default (created async race condition)

### Resource Flow

```
Cluster (topology.class) 
  → Runtime Extension finds GlobalInClusterIPPool (by labels)
    → Creates IPAddressClaim (with ownerReference to Cluster)
      → IPAM allocates IPAddress
        → Extension returns patch with IP
          → Cluster.spec.controlPlaneEndpoint.host set
            → InfrastructureCluster created with valid endpoint
```

## Development

```bash
# Build binary
make build

# Run tests
make test

# Build Docker image
make docker-build TAG=dev

# Run locally
go run ./cmd/capi-vip-allocator \
  --enable-runtime-extension=true \
  --runtime-extension-port=9443
```

## Roadmap

- [x] Control-plane VIP allocation via Runtime Extension
- [ ] Ingress VIP support (annotation-based)
- [ ] Prometheus metrics
- [ ] Events and Conditions
- [ ] Multi-namespace IP pools
- [ ] Helm chart

## License

Apache License 2.0

## Links

- [Cluster API Runtime Extensions](https://cluster-api.sigs.k8s.io/tasks/experimental-features/runtime-sdk/)
- [IPAM Provider](https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster)
- [Changelog](CHANGELOG.md)
