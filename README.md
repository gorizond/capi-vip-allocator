# CAPI VIP Allocator

Automatic Virtual IP allocation for Cluster API clusters using reconcile controller and custom ClusterClass variables.

> **Important:** This operator **only allocates** IP addresses from IPAM pools. To **install** the VIP on control plane nodes, you also need [kube-vip](https://kube-vip.io/) or similar solution (see [kube-vip integration](#kube-vip-integration)).

## Features

- **Automatic allocation** - Free IP from IPAM pool based on ClusterClass labels
- **Automatic cleanup** - IP released when Cluster is deleted via ownerReferences
- **Zero configuration** - User doesn't specify VIP in Cluster manifest
- **No race conditions** - Reconcile controller runs before topology reconcile
- **Custom variable** - VIP available in ClusterClass as `{{ .clusterVip }}`
- **Production ready** - TLS, health checks, leader election

## How it works (v0.5.0)

**Architecture: Reconcile Controller + Custom Variable + kube-vip**

```
┌───────────────────────────────────────────────────────────┐
│ MANAGEMENT CLUSTER                                        │
│                                                            │
│ 1. User creates Cluster (no VIP specified)               │
│    Cluster.spec.controlPlaneEndpoint.host = ""           │
│    Cluster.spec.topology.variables[clusterVip] = ""      │
│                                                            │
│ 2. Reconcile Controller (capi-vip-allocator)             │
│    ├─ Finds GlobalInClusterIPPool (by labels)            │
│    ├─ Creates IPAddressClaim                              │
│    ├─ Waits for IPAM to allocate free IP                 │
│    └─ Patches Cluster:                                    │
│       ├─ spec.controlPlaneEndpoint.host = "10.2.0.21"    │
│       └─ spec.topology.variables[clusterVip] = "10.2.0.21"│
│                                                            │
│ 3. Topology Controller (CAPI)                             │
│    ├─ Reads clusterVip variable                           │
│    ├─ Applies ClusterClass inline patches                │
│    └─ Creates InfrastructureCluster & ControlPlane       │
│                                                            │
└───────────────────────────────────────────────────────────┘
                          ↓
┌───────────────────────────────────────────────────────────┐
│ WORKLOAD CLUSTER                                          │
│                                                            │
│ 4. kube-vip DaemonSet                                     │
│    ├─ Deployed via ClusterClass patch (HelmChart)        │
│    ├─ Reads address from HelmChart: {{ .clusterVip }}    │
│    ├─ INSTALLS VIP on control plane node interface       │
│    └─ Provides HA for multi control plane clusters       │
│                                                            │
└───────────────────────────────────────────────────────────┘
```

**Result:** API server accessible via VIP, Rancher auto-import works! ✅

**Two components work together:**
- **capi-vip-allocator**: Allocates free IP from IPAM pool
- **kube-vip**: Installs VIP on control plane node interface

## Quick Start

### Prerequisites

1. **CAPI with ClusterTopology feature enabled** (enabled by default in CAPI v1.5+)

2. **IPAM provider installed** (e.g., in-cluster IPAM)

```bash
# Install in-cluster IPAM provider
clusterctl init --ipam in-cluster

# Or via CAPIProvider (Rancher Turtles)
kubectl apply -f - <<EOF
apiVersion: turtles-capi.cattle.io/v1alpha1
kind: CAPIProvider
metadata:
  name: ipam-in-cluster
  namespace: capi-system
spec:
  type: ipam
  version: v0.1.0
EOF
```

3. **cert-manager installed** (for Runtime Extension TLS - optional if using reconciler only)

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.0/cert-manager.yaml
```

### Installation

Using CAPIProvider (Rancher Turtles):

```yaml
apiVersion: turtles-capi.cattle.io/v1alpha1
kind: CAPIProvider
metadata:
  name: capi-vip-allocator
  namespace: capi-system
spec:
  type: addon
  version: v0.5.0
  fetchConfig:
    url: https://github.com/gorizond/capi-vip-allocator/releases/download/v0.5.0/capi-vip-allocator.yaml
```

Or directly:

```bash
kubectl apply -f https://github.com/gorizond/capi-vip-allocator/releases/download/v0.5.0/capi-vip-allocator.yaml
```

**Note:** v0.5.0 uses reconcile controller (not Runtime Extensions) by default.

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
  # DO NOT specify controlPlaneEndpoint! It will be set automatically
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
    # DO NOT specify clusterVip variable! It will be added automatically
    variables:
      - name: cni
        value: calico
```

**Done!** Within 5-10 seconds:
- `Cluster.spec.controlPlaneEndpoint.host` will be set (e.g., `10.0.0.15`)
- `Cluster.spec.topology.variables[clusterVip]` will be added (e.g., `10.0.0.15`)

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

### Configuration Options

Deployment args (v0.5.0+):

- `--enable-reconciler=true` - Enable reconcile controller (default: true, REQUIRED for VIP allocation)
- `--enable-runtime-extension=false` - Enable Runtime Extension mode (default: false, deprecated)
- `--runtime-extension-port=9443` - Runtime Extension server port
- `--leader-elect` - Enable leader election
- `--default-port=6443` - Default control plane port

**Important:** v0.5.0 uses **reconcile controller** architecture. Runtime Extensions are deprecated and disabled by default.

## ClusterClass Integration

### Step 1: Define `clusterVip` variable

The reconcile controller writes VIP to a **custom variable** `clusterVip`. You MUST define it in your ClusterClass:

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: ClusterClass
metadata:
  name: my-cluster-class
spec:
  variables:
    # REQUIRED: Define clusterVip variable for reconcile controller
    - name: clusterVip
      required: false
      schema:
        openAPIV3Schema:
          default: ""
          description: "Control plane VIP address (automatically set by capi-vip-allocator)"
          type: string
    
    # Your other variables...
    - name: cni
      schema:
        openAPIV3Schema:
          type: string
          default: calico
```

### Step 2: Use `{{ .clusterVip }}` in patches

Use the custom variable in ClusterClass patches to propagate VIP to infrastructure resources:

#### Patch InfrastructureCluster

```yaml
patches:
  - name: set-vip-on-infrastructure-cluster
    definitions:
      - jsonPatches:
          - op: replace
            path: /spec/template/spec/controlPlaneEndpoint/host
            valueFrom:
              template: "{{ .clusterVip }}"  # ✅ Uses custom variable
        selector:
          apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
          kind: ProxmoxClusterTemplate  # or your provider
          matchResources:
            infrastructureCluster: true
```

#### Patch ControlPlane (for TLS SANs and registration)

```yaml
  - name: set-control-plane-tls-san
    definitions:
      - jsonPatches:
          - op: add
            path: /spec/template/spec/serverConfig/tlsSAN/-
            valueFrom:
              template: "{{ .clusterVip }}"  # ✅ Uses custom variable
        selector:
          apiVersion: controlplane.cluster.x-k8s.io/v1beta1
          kind: RKE2ControlPlaneTemplate

  - name: set-control-plane-registration
    definitions:
      - jsonPatches:
          - op: replace
            path: /spec/template/spec/registrationAddress
            valueFrom:
              template: "{{ .clusterVip }}"  # ✅ Uses custom variable
        selector:
          apiVersion: controlplane.cluster.x-k8s.io/v1beta1
          kind: RKE2ControlPlaneTemplate
```

### Step 3: kube-vip Integration

> **Critical:** `capi-vip-allocator` **only allocates** IP addresses. You need **kube-vip** to actually **install** the VIP on control plane nodes!

Add kube-vip HelmChart via ClusterClass patch:

```yaml
patches:
  - name: install-kube-vip
    definitions:
      - jsonPatches:
          - op: add
            path: /spec/template/spec/files/-
            valueFrom:
              template: |
                path: /var/lib/rancher/rke2/server/manifests/kube-vip-apiserver.yaml
                permissions: "0644"
                owner: root:root
                content: |
                  apiVersion: helm.cattle.io/v1
                  kind: HelmChart
                  metadata:
                    name: kube-vip-apiserver
                    namespace: kube-system
                  spec:
                    version: 0.8.2
                    chart: kube-vip
                    repo: https://kube-vip.github.io/helm-charts
                    bootstrap: true
                    valuesContent: |-
                      nameOverride: kube-vip-apiserver
                      config:
                        address: '{{ .clusterVip }}'  # ✅ Uses allocated VIP
                      env:
                        vip_interface: ""  # Auto-detect
                        vip_arp: "true"
                        lb_enable: "false"
                        cp_enable: "true"
                        svc_enable: "false"
                        vip_leaderelection: "true"
                      nodeSelector:
                        node-role.kubernetes.io/control-plane: "true"
                      tolerations:
                        - key: "node-role.kubernetes.io/control-plane"
                          operator: "Exists"
                          effect: "NoSchedule"
        selector:
          apiVersion: controlplane.cluster.x-k8s.io/v1beta1
          kind: RKE2ControlPlaneTemplate  # or KubeadmControlPlaneTemplate
          matchResources:
            controlPlane: true
```

**What this does:**
1. Creates HelmChart manifest in `/var/lib/rancher/rke2/server/manifests/`
2. RKE2 (or kubeadm) applies it during bootstrap
3. kube-vip DaemonSet starts on control plane nodes
4. kube-vip installs VIP on node network interface
5. API server becomes accessible via VIP ✅

### Complete ClusterClass Example

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: ClusterClass
metadata:
  name: rke2-proxmox-class
spec:
  # 1. Define clusterVip variable (REQUIRED!)
  variables:
    - name: clusterVip
      required: false
      schema:
        openAPIV3Schema:
          default: ""
          description: "Control plane VIP (auto-allocated by capi-vip-allocator)"
          type: string
  
  # 2. Use {{ .clusterVip }} in patches
  patches:
    # Patch InfrastructureCluster
    - name: set-vip-on-proxmox-cluster
      definitions:
        - jsonPatches:
            - op: replace
              path: /spec/template/spec/controlPlaneEndpoint/host
              valueFrom:
                template: "{{ .clusterVip }}"
          selector:
            apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
            kind: ProxmoxClusterTemplate
            matchResources:
              infrastructureCluster: true
    
    # Patch ControlPlane for TLS SANs
    - name: set-rke2-tlssan
      definitions:
        - jsonPatches:
            - op: add
              path: /spec/template/spec/serverConfig/tlsSAN/-
              valueFrom:
                template: "{{ .clusterVip }}"
          selector:
            apiVersion: controlplane.cluster.x-k8s.io/v1beta1
            kind: RKE2ControlPlaneTemplate
            matchResources:
              controlPlane: true
    
    # Install kube-vip (REQUIRED to install VIP on node!)
    - name: install-kube-vip
      definitions:
        - jsonPatches:
            - op: add
              path: /spec/template/spec/files/-
              valueFrom:
                template: |
                  path: /var/lib/rancher/rke2/server/manifests/kube-vip-apiserver.yaml
                  permissions: "0644"
                  content: |
                    apiVersion: helm.cattle.io/v1
                    kind: HelmChart
                    metadata:
                      name: kube-vip-apiserver
                      namespace: kube-system
                    spec:
                      version: 0.8.2
                      chart: kube-vip
                      repo: https://kube-vip.github.io/helm-charts
                      bootstrap: true
                      valuesContent: |-
                        config:
                          address: '{{ .clusterVip }}'
                        env:
                          vip_interface: ""
                          vip_arp: "true"
                          cp_enable: "true"
          selector:
            apiVersion: controlplane.cluster.x-k8s.io/v1beta1
            kind: RKE2ControlPlaneTemplate
            matchResources:
              controlPlane: true
```

### Why Custom Variable Instead of Builtin?

**❌ Cannot use `{{ .builtin.cluster.spec.controlPlaneEndpoint.host }}`:**
- This builtin variable doesn't exist in CAPI

**❌ Cannot use `{{ .builtin.controlPlane.endpoint.host }}`:**
- Circular dependency: ControlPlane object is created **AFTER** InfrastructureCluster
- Value is empty during InfrastructureCluster creation

**✅ Custom variable `{{ .clusterVip }}` works:**
- Reconcile controller sets it **BEFORE** topology reconcile
- Available immediately for all patches
- No circular dependency!

## Troubleshooting

### VIP not allocated

Check the following:

1. **Operator is running:**
   ```bash
   kubectl get pods -n capi-system -l control-plane=capi-vip-allocator-controller-manager
   ```

2. **IP pool exists with correct labels:**
   ```bash
   kubectl get globalinclusterippool -l vip.capi.gorizond.io/cluster-class=YOUR_CLASS
   ```

3. **Pool has free IPs:**
   ```bash
   kubectl get globalinclusterippool POOL_NAME -o jsonpath='{.status.ipAddresses}'
   # Should show: {"free":9,"total":11,"used":2}
   ```

4. **Check operator logs:**
   ```bash
   kubectl logs -n capi-system -l control-plane=capi-vip-allocator-controller-manager -f
   
   # Should see:
   # INFO controllers.Cluster controlPlaneEndpoint not set, controller will allocate VIP
   # INFO controllers.Cluster control-plane VIP assigned ip=10.0.0.15
   ```

5. **Check IPAddressClaim:**
   ```bash
   kubectl get ipaddressclaim -n YOUR_NAMESPACE
   kubectl get ipaddress -n YOUR_NAMESPACE
   ```

### ClusterClass missing `clusterVip` variable

```
Error: ClusterClass variable 'clusterVip' not found
```

**Solution:** Add `clusterVip` variable to ClusterClass (see [Step 1](#step-1-define-clustervip-variable)).

### InfrastructureCluster validation error

```
Error: failed to create ProxmoxCluster.infrastructure.cluster.x-k8s.io: 
FieldValueInvalid: spec.controlplaneEndpoint: 
Invalid value: "<no value>": provided endpoint address is not a valid IP or FQDN
```

**Cause:** ClusterClass patch uses wrong variable or builtin.

**Solution:** Use `{{ .clusterVip }}` in your patch (see [Step 2](#step-2-use--clustervip--in-patches)).

### VIP allocated but not accessible

```bash
# Check if VIP is set in Cluster
kubectl get cluster my-cluster -o jsonpath='{.spec.controlPlaneEndpoint.host}'
# Returns: 10.0.0.15 ✅

# But API not accessible via VIP
curl -k https://10.0.0.15:6443/version
# Connection refused ❌
```

**Cause:** kube-vip is not installed in the workload cluster.

**Solution:** Add kube-vip HelmChart patch to ClusterClass (see [kube-vip integration](#step-3-kube-vip-integration)).

**Verify kube-vip is running:**
```bash
# SSH to control plane node
kubectl --kubeconfig /etc/rancher/rke2/rke2.yaml get pods -n kube-system | grep kube-vip
# Should show: kube-vip-apiserver-xxxxx  1/1  Running

# Check VIP is installed on interface
ip addr show ens18
# Should show: inet 10.0.0.15/32 scope global ens18
```

## Architecture (v0.5.0)

### Components

- **Reconcile Controller** - Watches Cluster resources with topology, allocates VIP before topology reconcile
- **IPAM Integration** - Creates/manages IPAddressClaim resources
- **Custom Variable** - Writes VIP to `Cluster.spec.topology.variables[clusterVip]`
- **ownerReferences** - Automatic cleanup when Cluster is deleted
- **Runtime Extension** (optional, deprecated) - Kept for backward compatibility

### Resource Flow

```
User creates Cluster
  ↓
Reconcile Controller watches
  ├─ Finds GlobalInClusterIPPool (by ClusterClass labels)
  ├─ Creates IPAddressClaim (with ownerReference)
  ├─ Waits for IPAM to allocate IPAddress
  └─ Patches Cluster:
     ├─ spec.controlPlaneEndpoint.host = VIP
     └─ spec.topology.variables[clusterVip] = VIP
  ↓
Topology Controller reconciles
  ├─ Reads clusterVip variable
  ├─ Applies ClusterClass inline patches
  └─ Creates InfrastructureCluster with VIP
  ↓
ControlPlane bootstrap
  ├─ Applies HelmChart manifest (kube-vip)
  └─ kube-vip installs VIP on node interface
  ↓
API server accessible via VIP ✅
```

### Why v0.5.0 Architecture?

**v0.2.x - v0.4.x attempts failed:**

- ❌ **v0.2.x**: Reconcile controller ran async → race condition
- ❌ **v0.3.x**: BeforeClusterCreate hook → cannot modify Cluster object
- ❌ **v0.4.x**: GeneratePatches external patch → limited to `spec.template.spec` fields
- ❌ **Builtin variables**: Circular dependency (ControlPlane created after InfrastructureCluster)

**✅ v0.5.0 solution:**
- Reconcile controller runs **before** topology reconcile
- Custom variable `clusterVip` breaks circular dependency
- kube-vip handles VIP installation (not the operator)

## Development

```bash
# Build binary
make build

# Run tests
make test

# Build Docker image
make docker-build TAG=dev

# Run locally (v0.5.0)
go run ./cmd/capi-vip-allocator \
  --enable-reconciler=true \
  --enable-runtime-extension=false \
  --default-port=6443
```

## Roadmap

- [x] Control-plane VIP allocation via reconcile controller
- [x] Custom variable integration with ClusterClass
- [x] kube-vip integration example
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
