# capi-vip-allocator

**capi-vip-allocator** is a lightweight Wrangler-based operator for automatic allocation of Virtual IPs (VIP) in Cluster API–managed Kubernetes clusters.
It dynamically assigns IP addresses for control-plane endpoints and ingress controllers using in-cluster IPAM pools — eliminating the need for manual VIP configuration in Rancher + Turtles environments.

## Features

* Automatically allocates a VIP for `spec.topology.controlPlaneEndpoint.host`
* Supports IP allocation from `GlobalInClusterIPPool` and `InClusterIPPool` (via labels)
* Integrates seamlessly with Rancher, Cluster API, and Turtles
* Optional VIP allocation for ingress controllers and other components
* Managed lifecycle — IPs are released when clusters are deleted

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
