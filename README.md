# CAPI VIP Allocator

Automatic Virtual IP allocation for Cluster API clusters using Runtime Extensions.

## Features

- **Zero race condition** - VIP allocated BEFORE Cluster is written to etcd
- **Automatic allocation** - IP from IPAM pool based on ClusterClass labels
- **Automatic cleanup** - IP released when Cluster is deleted
- **CAPI native** - Uses Runtime Extensions API
- **Production ready** - TLS, health checks, metrics

## How it works

```
Client creates Cluster (host: "")
         ↓
CAPI Topology Controller calls GeneratePatches hook
         ↓
VIP Allocator Extension
  1. Finds IP pool by cluster-class label
  2. Creates IPAddressClaim
  3. Waits for IPAM to allocate IP (~5 sec)
  4. Returns patch: {host: "10.2.0.20"}
         ↓
Cluster saved to etcd WITH IP ✅
         ↓
InfrastructureCluster created successfully ✅
```

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
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.0/cert-manager.yaml
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
  version: v0.1.0
  fetchConfig:
    url: https://github.com/gorizond/capi-vip-allocator/releases/download/v0.1.0/capi-vip-allocator.yaml
```

**Note:** Runtime Extensions are registered via `ExtensionConfig` resource (included in the manifest), not through CAPIProvider type.

Or directly:

```bash
kubectl apply -f https://github.com/gorizond/capi-vip-allocator/releases/download/v0.1.0/capi-vip-allocator.yaml
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
    version: v1.30.0
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

Deployment args:

- `--enable-runtime-extension=true` - Enable Runtime Extension mode (default: true)
- `--runtime-extension-port=9443` - Runtime Extension server port
- `--leader-elect` - Enable leader election
- `--default-port=6443` - Default control plane port

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

## Architecture

### Components

- **Runtime Extension Server** - HTTP server (port 9443) handling CAPI hooks
- **GeneratePatches Hook** - Allocates VIP synchronously during topology reconciliation
- **Reconciler** - Fallback mode and claim adoption (ownerReferences)
- **IPAM Integration** - Creates/manages IPAddressClaim resources

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
