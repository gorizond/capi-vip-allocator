package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClusterReconciler_Reconcile_RequeuesWhenClaimPending(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	registerIPAMGVKs(scheme)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: clusterv1.ClusterSpec{
			Topology: &clusterv1.Topology{Class: "example"},
		},
	}

	pool := newGlobalPool("pool-cp", map[string]string{
		clusterClassLabel: "example",
		roleLabel:         controlPlaneRole,
	})

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cluster, pool).Build()
	reconciler := &ClusterReconciler{
		Client:      client,
		Scheme:      scheme,
		Logger:      testr.New(t),
		DefaultPort: 6443,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}}

	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}
	if result.RequeueAfter != defaultRequeueDelay {
		t.Fatalf("expected requeue after %v, got %v", defaultRequeueDelay, result.RequeueAfter)
	}

	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind})
	if err := client.Get(ctx, types.NamespacedName{Name: "vip-cp-" + cluster.Name, Namespace: cluster.Namespace}, claim); err != nil {
		t.Fatalf("expected IPAddressClaim to be created: %v", err)
	}

	if got := claim.GetLabels()[roleLabel]; got != controlPlaneRole {
		t.Fatalf("expected claim label %q=%q, got %q", roleLabel, controlPlaneRole, got)
	}

	poolRef, found, err := unstructured.NestedMap(claim.Object, "spec", "poolRef")
	if err != nil {
		t.Fatalf("read poolRef: %v", err)
	}
	if !found {
		t.Fatalf("poolRef not set on claim")
	}
	if name, ok := poolRef["name"].(string); !ok || name != pool.GetName() {
		t.Fatalf("expected poolRef name %q, got %#v", pool.GetName(), poolRef["name"])
	}

	owners := claim.GetOwnerReferences()
	if len(owners) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(owners))
	}
	if owners[0].Name != cluster.Name {
		t.Fatalf("unexpected owner reference name: %s", owners[0].Name)
	}

	updatedCluster := &clusterv1.Cluster{}
	if err := client.Get(ctx, req.NamespacedName, updatedCluster); err != nil {
		t.Fatalf("fetch cluster after reconcile: %v", err)
	}
	if updatedCluster.Spec.ControlPlaneEndpoint.Host != "" {
		t.Fatalf("expected control plane endpoint host to remain empty")
	}
}

func TestClusterReconciler_Reconcile_AssignsIPAddress(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	registerIPAMGVKs(scheme)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: clusterv1.ClusterSpec{
			Topology: &clusterv1.Topology{Class: "example"},
		},
	}

	pool := newGlobalPool("pool-cp", map[string]string{
		clusterClassLabel: "example",
		roleLabel:         controlPlaneRole,
	})

	claim := newIPAddressClaim(cluster, "vip-cp-"+cluster.Name)
	if err := unstructured.SetNestedField(claim.Object, map[string]interface{}{
		"name": "vip-address",
	}, "status", "addressRef"); err != nil {
		t.Fatalf("set claim status: %v", err)
	}

	ip := newIPAddress("vip-address", cluster.Namespace, "10.0.0.15")

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cluster, pool, claim, ip).Build()
	reconciler := &ClusterReconciler{
		Client:      client,
		Scheme:      scheme,
		Logger:      testr.New(t),
		DefaultPort: 6443,
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}}

	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expected no requeue, got %v", result.RequeueAfter)
	}

	updatedCluster := &clusterv1.Cluster{}
	if err := client.Get(ctx, req.NamespacedName, updatedCluster); err != nil {
		t.Fatalf("fetch cluster after reconcile: %v", err)
	}
	if updatedCluster.Spec.ControlPlaneEndpoint.Host != "10.0.0.15" {
		t.Fatalf("expected control plane endpoint host to be 10.0.0.15, got %s", updatedCluster.Spec.ControlPlaneEndpoint.Host)
	}
	if updatedCluster.Spec.ControlPlaneEndpoint.Port != 6443 {
		t.Fatalf("expected control plane endpoint port to default to 6443, got %d", updatedCluster.Spec.ControlPlaneEndpoint.Port)
	}
}

func TestEnsureClaimErrorsWhenPoolMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	registerIPAMGVKs(scheme)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-pool",
			Namespace: "default",
		},
		Spec: clusterv1.ClusterSpec{
			Topology: &clusterv1.Topology{Class: "missing"},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cluster).Build()
	reconciler := &ClusterReconciler{
		Client: client,
		Scheme: scheme,
		Logger: testr.New(t),
	}

	_, err := reconciler.ensureClaim(context.Background(), cluster, "vip-cp-"+cluster.Name)
	if err == nil {
		t.Fatalf("expected error when pool is missing")
	}
	if !strings.Contains(err.Error(), "no matching ip pool") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFindPoolMatchesClusterClassAndRole(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	registerIPAMGVKs(scheme)

	matching := newGlobalPool("control-plane-pool", map[string]string{
		clusterClassLabel: "prod",
		roleLabel:         controlPlaneRole,
	})
	wrongClass := newGlobalPool("wrong-class", map[string]string{
		clusterClassLabel: "dev",
		roleLabel:         controlPlaneRole,
	})
	wrongRole := newGlobalPool("wrong-role", map[string]string{
		clusterClassLabel: "prod",
		roleLabel:         "ingress",
	})

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(matching, wrongClass, wrongRole).
		Build()

	reconciler := &ClusterReconciler{
		Client: client,
		Scheme: scheme,
		Logger: testr.New(t),
	}

	got, err := reconciler.findPool(context.Background(), "prod", controlPlaneRole)
	if err != nil {
		t.Fatalf("findPool returned error: %v", err)
	}
	if got != matching.GetName() {
		t.Fatalf("expected pool %q, got %q", matching.GetName(), got)
	}
}

func TestPatchClusterEndpointPreservesExistingPort(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	registerIPAMGVKs(scheme)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-with-port",
			Namespace: "default",
		},
		Spec: clusterv1.ClusterSpec{
			Topology: &clusterv1.Topology{Class: "example"},
			ControlPlaneEndpoint: clusterv1.APIEndpoint{
				Port: 7443,
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cluster).Build()
	reconciler := &ClusterReconciler{
		Client:      client,
		Scheme:      scheme,
		Logger:      testr.New(t),
		DefaultPort: 6443,
	}

	if err := reconciler.patchClusterEndpoint(context.Background(), cluster, "10.1.1.10"); err != nil {
		t.Fatalf("patchClusterEndpoint returned error: %v", err)
	}

	updated := &clusterv1.Cluster{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, updated); err != nil {
		t.Fatalf("fetch cluster after patch: %v", err)
	}

	if updated.Spec.ControlPlaneEndpoint.Host != "10.1.1.10" {
		t.Fatalf("expected host to be updated, got %s", updated.Spec.ControlPlaneEndpoint.Host)
	}
	if updated.Spec.ControlPlaneEndpoint.Port != 7443 {
		t.Fatalf("expected control plane port to remain 7443, got %d", updated.Spec.ControlPlaneEndpoint.Port)
	}
}

func TestResolveIPAddressPendingWithoutIPAddressResource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	registerIPAMGVKs(scheme)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wait-for-ip",
			Namespace: "default",
		},
	}

	claim := newIPAddressClaim(cluster, "vip-cp-"+cluster.Name)
	if err := unstructured.SetNestedField(claim.Object, map[string]interface{}{
		"name": "pending-ip",
	}, "status", "addressRef"); err != nil {
		t.Fatalf("set claim status: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(claim).Build()
	reconciler := &ClusterReconciler{
		Client: client,
		Scheme: scheme,
		Logger: testr.New(t),
	}

	ip, ready, err := reconciler.resolveIPAddress(context.Background(), cluster.Namespace, claim)
	if err != nil {
		t.Fatalf("resolveIPAddress returned error: %v", err)
	}
	if ready {
		t.Fatalf("expected claim to be pending, got ready with ip %q", ip)
	}
	if ip != "" {
		t.Fatalf("expected ip to be empty while pending, got %q", ip)
	}
}

func registerIPAMGVKs(scheme *runtime.Scheme) {
	gv := schema.GroupVersion{Group: ipamGroup, Version: ipamVersion}
	scheme.AddKnownTypeWithName(gv.WithKind(globalPoolKind), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gv.WithKind(globalPoolKind+"List"), &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(gv.WithKind(ipAddressClaimKind), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gv.WithKind(ipAddressClaimKind+"List"), &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(gv.WithKind(ipAddressKind), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gv.WithKind(ipAddressKind+"List"), &unstructured.UnstructuredList{})
}

func newGlobalPool(name string, labels map[string]string) *unstructured.Unstructured {
	pool := &unstructured.Unstructured{}
	pool.SetGroupVersionKind(schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: globalPoolKind})
	pool.SetName(name)
	pool.SetLabels(labels)
	return pool
}

func newIPAddressClaim(cluster *clusterv1.Cluster, name string) *unstructured.Unstructured {
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressClaimKind})
	claim.SetName(name)
	claim.SetNamespace(cluster.Namespace)
	claim.SetLabels(map[string]string{roleLabel: controlPlaneRole})
	ownerRef := metav1.NewControllerRef(cluster, clusterv1.GroupVersion.WithKind("Cluster"))
	claim.SetOwnerReferences([]metav1.OwnerReference{*ownerRef})
	if err := unstructured.SetNestedField(claim.Object, map[string]interface{}{
		"apiVersion": gvString(),
		"kind":       globalPoolKind,
		"name":       "pool-cp",
	}, "spec", "poolRef"); err != nil {
		panic(err)
	}
	return claim
}

func newIPAddress(name, namespace, address string) *unstructured.Unstructured {
	ip := &unstructured.Unstructured{}
	ip.SetGroupVersionKind(schema.GroupVersionKind{Group: ipamGroup, Version: ipamVersion, Kind: ipAddressKind})
	ip.SetName(name)
	ip.SetNamespace(namespace)
	if err := unstructured.SetNestedField(ip.Object, address, "spec", "address"); err != nil {
		panic(err)
	}
	return ip
}

func gvString() string {
	return ipamGroup + "/" + ipamVersion
}
