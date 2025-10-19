# CAPI VIP Allocator - Full GitOps Deployment

Complete structure for deploying **ExtensionConfig** and all dependent resources via **Fleet GitOps**.

## 📋 What's Included

This example includes all necessary resources to register CAPI VIP Allocator as a Runtime Extension:

1. **issuer.yaml** - Self-signed Issuer for cert-manager
2. **certificate.yaml** - TLS certificate for Runtime Extension
3. **service.yaml** - Kubernetes Service for accessing Runtime Extension
4. **extensionconfig.yaml** - Runtime Extension registration in CAPI
5. **fleet.yaml** - Fleet configuration for deployment
6. **example-gitrepo.yaml** - Example GitRepo for Fleet

## 🎯 Prerequisites

### The management cluster must have installed:

1. **Cluster API (CAPI)** - core provider
   ```bash
   kubectl get deployment -n capi-system capi-controller-manager
   ```

2. **cert-manager** - for certificate generation
   ```bash
   kubectl get deployment -n cert-manager cert-manager
   ```

3. **CAPI VIP Allocator Deployment** - the controller itself must be deployed
   ```bash
   kubectl get deployment -n capi-system capi-vip-allocator-controller-manager
   ```

4. **Fleet** - for GitOps (if using Rancher)
   ```bash
   kubectl get deployment -n cattle-fleet-system fleet-controller
   ```

## 🚀 Deployment

### Via Fleet (recommended)

1. **Copy this directory** to your Fleet GitOps repository:
   ```bash
   cp -r examples/fleet-gitops-full/ /path/to/your/fleet-repo/capi-vip-allocator-extensionconfig/
   cd /path/to/your/fleet-repo/
   ```

2. **Commit and push** changes:
   ```bash
   git add capi-vip-allocator-extensionconfig/
   git commit -m "feat: add CAPI VIP Allocator ExtensionConfig"
   git push
   ```

3. **Create Fleet GitRepo** in Rancher or via kubectl:
   
   Edit `example-gitrepo.yaml` and replace repository URL, then:
   ```bash
   kubectl apply -f example-gitrepo.yaml
   ```

4. **Check deployment status**:
   ```bash
   # Check Fleet GitRepo
   kubectl get gitrepo -n fleet-default capi-vip-allocator-extensionconfig
   
   # Check Bundle
   kubectl get bundle -n fleet-local
   ```

### Via kubectl (if not using Fleet)

Apply directly:

```bash
# From this directory
kubectl apply -f issuer.yaml
kubectl apply -f certificate.yaml
kubectl apply -f service.yaml
kubectl apply -f extensionconfig.yaml
```

## ✅ Deployment Verification

### 1. Check Certificate

```bash
# Certificate should be Ready
kubectl get certificate -n capi-system capi-vip-allocator-runtime-extension-cert

# Secret with TLS certificate should exist
kubectl get secret -n capi-system capi-vip-allocator-runtime-extension-tls
```

Expected output:
```
NAME                                            READY   SECRET                                       AGE
capi-vip-allocator-runtime-extension-cert       True    capi-vip-allocator-runtime-extension-tls     5m
```

### 2. Check Service

```bash
kubectl get service -n capi-system capi-vip-allocator-runtime-extension
```

Expected output:
```
NAME                                     TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)   AGE
capi-vip-allocator-runtime-extension     ClusterIP   10.43.xxx.xxx   <none>        443/TCP   5m
```

### 3. Check ExtensionConfig

```bash
# Check existence
kubectl get extensionconfig -n capi-system vip-allocator

# Check details (caBundle should be present)
kubectl get extensionconfig -n capi-system vip-allocator -o yaml
```

Expected output:
```yaml
apiVersion: runtime.cluster.x-k8s.io/v1alpha1
kind: ExtensionConfig
metadata:
  name: vip-allocator
  namespace: capi-system
spec:
  clientConfig:
    caBundle: LS0tLS1CRUdJTi...  # ← should be populated!
    service:
      name: capi-vip-allocator-runtime-extension
      namespace: capi-system
      port: 443
status:
  conditions:
  - type: Discovered
    status: "True"
    reason: Discovered
```

### 4. Check CAPI Controller Manager

Check CAPI controller-manager logs - there should be no errors about ExtensionConfig:

```bash
kubectl logs -n capi-system deployment/capi-controller-manager -f | grep -i "extension\|vip"
```

Expected messages (WITHOUT errors):
```
I1018 16:20:00.123456       1 controller.go:123] "Discovered runtime extension" name="vip-allocator"
```

**Should NOT see:**
```
E1018 16:19:50.532349       1 main.go:433] "ExtensionConfig registry warmup timed out" ← BAD!
```

### 5. Check Extension Registration

```bash
# Check ExtensionConfig status
kubectl get extensionconfig vip-allocator -n capi-system -o jsonpath='{.status.conditions[?(@.type=="Discovered")].status}'
```

Should return: `True`

## 🔧 Troubleshooting

### Issue: Certificate not Ready

```bash
# Check Certificate status
kubectl describe certificate -n capi-system capi-vip-allocator-runtime-extension-cert

# Check CertificateRequest
kubectl get certificaterequest -n capi-system

# Check cert-manager logs
kubectl logs -n cert-manager deployment/cert-manager
```

**Solution:** Ensure cert-manager is installed and running.

### Issue: caBundle not populated in ExtensionConfig

**Cause:** CAPI controller-manager cannot read Secret with certificate.

**Solution:**
1. Check that Secret exists:
   ```bash
   kubectl get secret -n capi-system capi-vip-allocator-runtime-extension-tls
   ```

2. Check that Secret contains `ca.crt`:
   ```bash
   kubectl get secret -n capi-system capi-vip-allocator-runtime-extension-tls -o jsonpath='{.data.ca\.crt}' | base64 -d
   ```

3. Restart CAPI controller-manager:
   ```bash
   kubectl rollout restart deployment -n capi-system capi-controller-manager
   ```

### Issue: CAPI Controller Manager error "ExtensionConfig registry warmup timed out"

**Cause:** ExtensionConfig was not created before CAPI controller-manager started.

**Solution:**
1. Check that ExtensionConfig is created:
   ```bash
   kubectl get extensionconfig -n capi-system vip-allocator
   ```

2. If not - apply manifests:
   ```bash
   kubectl apply -f extensionconfig.yaml
   ```

3. Restart CAPI controller-manager:
   ```bash
   kubectl rollout restart deployment -n capi-system capi-controller-manager
   ```

### Issue: Service cannot find pods

**Cause:** Service selector doesn't match pod labels.

**Solution:**
```bash
# Check labels on pods
kubectl get pods -n capi-system -l cluster.x-k8s.io/provider=vip-allocator --show-labels

# Check Service selector
kubectl get service -n capi-system capi-vip-allocator-runtime-extension -o yaml | grep -A 3 selector
```

## 🗑️ Cleaning Up (if recreating)

If you need to recreate resources:

```bash
# Delete in reverse order
kubectl delete extensionconfig -n capi-system vip-allocator
kubectl delete service -n capi-system capi-vip-allocator-runtime-extension
kubectl delete certificate -n capi-system capi-vip-allocator-runtime-extension-cert
kubectl delete secret -n capi-system capi-vip-allocator-runtime-extension-tls
kubectl delete issuer -n capi-system capi-vip-allocator-selfsigned-issuer
```

**⚠️ IMPORTANT:** DO NOT delete Deployment `capi-vip-allocator-controller-manager` - it should keep running!

## 📦 File Structure

```
fleet-gitops-full/
├── README.md              # This guide
├── fleet.yaml             # Fleet configuration (target clusters)
├── issuer.yaml            # Self-signed Issuer for cert-manager
├── certificate.yaml       # TLS Certificate for Runtime Extension
├── service.yaml           # Service for accessing Runtime Extension
├── extensionconfig.yaml   # Runtime Extension registration in CAPI
└── example-gitrepo.yaml   # Example GitRepo for Fleet
```

## 🔄 Resource Application Order

Fleet applies resources automatically in alphabetical order:

1. **certificate.yaml** → requests certificate from issuer
2. **extensionconfig.yaml** → registers Runtime Extension
3. **issuer.yaml** → creates self-signed issuer
4. **service.yaml** → creates endpoint for Runtime Extension

Note: Fleet handles dependencies automatically, waiting for resources to become ready.

## 💡 Simplified Approach

If you already have Certificate and Service deployed via CAPIProvider:

1. **Keep only** `extensionconfig.yaml` and `fleet.yaml`
2. **Delete** other files (issuer, certificate, service)
3. Fleet will just create the missing ExtensionConfig

This is the simplest approach - don't touch working resources!

## 📚 Additional Resources

- [Cluster API Runtime SDK](https://cluster-api.sigs.k8s.io/tasks/experimental-features/runtime-sdk/index.html)
- [cert-manager Documentation](https://cert-manager.io/docs/)
- [Fleet GitOps Documentation](https://fleet.rancher.io/)

## 🤝 Support

If you encounter issues:

1. Check Fleet controller logs:
   ```bash
   kubectl logs -n cattle-fleet-system deployment/fleet-controller -f
   ```

2. Check Bundle status:
   ```bash
   kubectl describe bundle -n fleet-local <bundle-name>
   ```

3. Create an issue in the repository: [GitHub Issues](https://github.com/gorizond/capi-vip-allocator/issues)
