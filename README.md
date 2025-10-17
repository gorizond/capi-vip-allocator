# capi-vip-allocator

**capi-vip-allocator** is a lightweight Wrangler-based operator for automatic allocation of Virtual IPs (VIP) in Cluster API–managed Kubernetes clusters.
The current MVP focuses on the control-plane VIP lifecycle and integrates with the Cluster API IPAM provider to remove manual endpoint management in Rancher + Turtles environments.

## Features

* Automatically allocates a VIP for `spec.topology.controlPlaneEndpoint.host`
* Reuses existing `GlobalInClusterIPPool` objects selected by `vip.capi.gorizond.io/cluster-class`
* Creates an `IPAddressClaim` owned by the target `Cluster`
* Waits for the resolved `IPAddress` and patches the control-plane endpoint
* On deletion the claim is garbage-collected with the `Cluster`

> **MVP note**: ingress VIP management, metrics, events and condition reporting are not yet implemented.

## Quick start

```bash
go build ./cmd/capi-vip-allocator
# or run: go run ./cmd/capi-vip-allocator
./capi-vip-allocator \
  --metrics-bind-address=:8080 \
  --health-probe-bind-address=:8081
```

By default the controller looks for pools with labels:

* `vip.capi.gorizond.io/cluster-class=<clusterClass>`
* `vip.capi.gorizond.io/role=control-plane`

Deploy matching `GlobalInClusterIPPool` resources before creating `Cluster` objects with topology classes.

## Architecture Overview

```
Cluster → IPAddressClaim (labeled)
           ↓
capi-vip-allocator (Wrangler Operator)
           ↓
GlobalInClusterIPPool / InClusterIPPool
           ↓
Allocated IP → Patched into Cluster.spec.topology.controlPlaneEndpoint.host
```


## Configuration

Create or label an existing IP pool to be used for VIP allocation:

```yaml
apiVersion: ipam.cluster.x-k8s.io/v1alpha2
kind: GlobalInClusterIPPool
metadata:
  name: vip-pool
  labels:
    vip.capi.gorizond.io/cluster-class: rke2-cluster-class
spec:
  addresses:
    - 10.0.0.10-10.0.0.20
  prefix: 24
```

When a new Cluster is created using the specified `ClusterClass`,
`capi-vip-allocator` automatically allocates a VIP and updates the control-plane endpoint.

## Example

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: demo-cluster
  namespace: clusters
spec:
  topology:
    class: rke2-cluster-class
    controlPlane:
      replicas: 1
    controlPlaneEndpoint:
      host: ""   # Filled automatically by capi-vip-allocator
      port: 6443
```
