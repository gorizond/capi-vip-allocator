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

func TestClusterReconciler_Reconcile_AssignsIPAddress_DirectMode(t *testing.T) {
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

	// ClusterClass without clusterVip variable (direct mode)
	clusterClass := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example",
		},
		Spec: clusterv1.ClusterClassSpec{
			Variables: []clusterv1.ClusterClassVariable{
				{Name: "someOtherVariable"},
			},
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

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cluster, clusterClass, pool, claim, ip).Build()
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

	// In direct mode, clusterVip variable should NOT be added
	for _, v := range updatedCluster.Spec.Topology.Variables {
		if v.Name == "clusterVip" {
			t.Fatalf("clusterVip variable should not be added in direct mode")
		}
	}
}

func TestClusterReconciler_Reconcile_AssignsIPAddress_LegacyMode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}
	registerIPAMGVKs(scheme)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-legacy",
			Namespace: "default",
		},
		Spec: clusterv1.ClusterSpec{
			Topology: &clusterv1.Topology{Class: "example-legacy"},
		},
	}

	// ClusterClass WITH clusterVip variable (legacy mode)
	clusterClass := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example-legacy",
		},
		Spec: clusterv1.ClusterClassSpec{
			Variables: []clusterv1.ClusterClassVariable{
				{Name: "clusterVip"},
				{Name: "someOtherVariable"},
			},
		},
	}

	pool := newGlobalPool("pool-cp", map[string]string{
		clusterClassLabel: "example-legacy",
		roleLabel:         controlPlaneRole,
	})

	claim := newIPAddressClaim(cluster, "vip-cp-"+cluster.Name)
	if err := unstructured.SetNestedField(claim.Object, map[string]interface{}{
		"name": "vip-address",
	}, "status", "addressRef"); err != nil {
		t.Fatalf("set claim status: %v", err)
	}

	ip := newIPAddress("vip-address", cluster.Namespace, "10.0.0.20")

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cluster, clusterClass, pool, claim, ip).Build()
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
	if updatedCluster.Spec.ControlPlaneEndpoint.Host != "10.0.0.20" {
		t.Fatalf("expected control plane endpoint host to be 10.0.0.20, got %s", updatedCluster.Spec.ControlPlaneEndpoint.Host)
	}
	if updatedCluster.Spec.ControlPlaneEndpoint.Port != 6443 {
		t.Fatalf("expected control plane endpoint port to default to 6443, got %d", updatedCluster.Spec.ControlPlaneEndpoint.Port)
	}

	// In legacy mode, clusterVip variable SHOULD be added
	foundVipVariable := false
	for _, v := range updatedCluster.Spec.Topology.Variables {
		if v.Name == "clusterVip" {
			foundVipVariable = true
			if string(v.Value.Raw) != `"10.0.0.20"` {
				t.Fatalf("expected clusterVip variable to be %q, got %q", `"10.0.0.20"`, string(v.Value.Raw))
			}
		}
	}
	if !foundVipVariable {
		t.Fatalf("clusterVip variable should be added in legacy mode")
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

	// ClusterClass without clusterVip variable (direct mode)
	clusterClass := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example",
		},
		Spec: clusterv1.ClusterClassSpec{
			Variables: []clusterv1.ClusterClassVariable{
				{Name: "someOtherVariable"},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cluster, clusterClass).Build()
	reconciler := &ClusterReconciler{
		Client:      client,
		Scheme:      scheme,
		Logger:      testr.New(t),
		DefaultPort: 6443,
	}

	if err := reconciler.patchClusterEndpoint(context.Background(), cluster, "10.1.1.10", cluster.Namespace); err != nil {
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
	// Register pool types with v1alpha2
	gvPool := schema.GroupVersion{Group: ipamGroup, Version: globalPoolAPIVersion}
	scheme.AddKnownTypeWithName(gvPool.WithKind(globalPoolKind), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvPool.WithKind(globalPoolKind+"List"), &unstructured.UnstructuredList{})

	// Register claim/address types with v1beta1
	gv := schema.GroupVersion{Group: ipamGroup, Version: ipamVersion}
	scheme.AddKnownTypeWithName(gv.WithKind(ipAddressClaimKind), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gv.WithKind(ipAddressClaimKind+"List"), &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(gv.WithKind(ipAddressKind), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gv.WithKind(ipAddressKind+"List"), &unstructured.UnstructuredList{})
}

func newGlobalPool(name string, labels map[string]string) *unstructured.Unstructured {
	pool := &unstructured.Unstructured{}
	pool.SetGroupVersionKind(schema.GroupVersionKind{Group: ipamGroup, Version: globalPoolAPIVersion, Kind: globalPoolKind})
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

func TestGetClusterClass_NamespaceScoped(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}

	// ClusterClass in namespace
	clusterClass := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rke2-proxmox-class",
			Namespace: "clusters-proxmox",
		},
		Spec: clusterv1.ClusterClassSpec{
			Variables: []clusterv1.ClusterClassVariable{
				{Name: "clusterVip"},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(clusterClass).Build()
	reconciler := &ClusterReconciler{
		Client: client,
		Scheme: scheme,
		Logger: testr.New(t),
	}

	ctx := context.Background()

	// Test: should find ClusterClass in namespace
	got, err := reconciler.getClusterClass(ctx, "rke2-proxmox-class", "clusters-proxmox")
	if err != nil {
		t.Fatalf("getClusterClass returned error: %v", err)
	}
	if got.Name != "rke2-proxmox-class" {
		t.Fatalf("expected ClusterClass name %q, got %q", "rke2-proxmox-class", got.Name)
	}
	if got.Namespace != "clusters-proxmox" {
		t.Fatalf("expected ClusterClass namespace %q, got %q", "clusters-proxmox", got.Namespace)
	}
}

func TestGetClusterClass_ClusterScoped(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cluster api scheme: %v", err)
	}

	// ClusterClass without namespace (cluster-scoped)
	clusterClass := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "global-class",
		},
		Spec: clusterv1.ClusterClassSpec{
			Variables: []clusterv1.ClusterClassVariable{
				{Name: "someVariable"},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(clusterClass).Build()
	reconciler := &ClusterReconciler{
		Client: client,
		Scheme: scheme,
		Logger: testr.New(t),
	}

	ctx := context.Background()

	// Test: should find cluster-scoped ClusterClass
	got, err := reconciler.getClusterClass(ctx, "global-class", "any-namespace")
	if err != nil {
		t.Fatalf("getClusterClass returned error: %v", err)
	}
	if got.Name != "global-class" {
		t.Fatalf("expected ClusterClass name %q, got %q", "global-class", got.Name)
	}
}
