# ExtensionConfig with CA Bundle - GitOps Strategy

## Problem

`cert-manager` cainjector **does not support** automatic CA injection into `ExtensionConfig` resources. The annotation `cert-manager.io/inject-ca-from` only works for:
- MutatingWebhookConfiguration
- ValidatingWebhookConfiguration  
- APIService
- CustomResourceDefinition

However, **CAPI controller-manager** has a mutating webhook (`default.extensionconfig.runtime.addons.cluster.x-k8s.io`) that SHOULD automatically inject `caBundle` based on the annotation.

## Root Cause

The `caBundle` is not injected because:
1. ExtensionConfig is created by CAPIProvider (Rancher Turtles) BEFORE the Certificate is ready
2. The mutating webhook only processes CREATE/UPDATE operations
3. Once created without `caBundle`, it's not automatically updated

## Solution: Two-Stage GitOps Deployment

### Stage 1: CAPIProvider Resources (GitOps Repo 1)

Deploy all core resources:
```yaml
# Via Rancher Turtles CAPIProvider
apiVersion: turtles-capi.cattle.io/v1alpha1
kind: CAPIProvider
metadata:
  name: capi-vip-allocator
  namespace: capi-system
spec:
  name: vip-allocator
  type: addon
  version: v0.1.0
```

This creates:
- Deployment (capi-vip-allocator-controller-manager)
- Services
- RBAC (ServiceAccount, Roles, RoleBindings)
- Certificate + Issuer
- Secret with TLS certificates (including CA)

**Wait for**: Certificate Ready status

### Stage 2: ExtensionConfig (GitOps Repo 2)

Deploy **only** ExtensionConfig after Stage 1 is ready.

**IMPORTANT**: Do NOT include ExtensionConfig in Stage 1 CAPIProvider manifest!

#### Option A: Separate CAPIProvider manifest (Recommended)

Create custom manifest WITHOUT ExtensionConfig:
1. Remove ExtensionConfig from `capi-vip-allocator.yaml`
2. Use this example for separate deployment

#### Option B: Deploy after CAPIProvider (Simple)

Just deploy this ExtensionConfig as separate Fleet GitRepo:

```yaml
# Fleet GitRepo - Stage 2
apiVersion: fleet.cattle.io/v1alpha1
kind: GitRepo
metadata:
  name: capi-vip-allocator-extensionconfig
  namespace: fleet-default
spec:
  repo: https://github.com/gorizond/capi-vip-allocator
  branch: main
  paths:
    - examples/extensionconfig-with-ca
  targets:
    - clusterSelector:
        matchLabels:
          name: local  # Rancher local cluster
  # Wait for Certificate to be Ready
  dependsOn:
    - name: capi-vip-allocator-provider
```

#### How It Works

When ExtensionConfig is created:
1. CAPI controller-manager mutating webhook intercepts the CREATE request
2. Webhook reads annotation `cert-manager.io/inject-ca-from`
3. Webhook fetches CA from Secret `capi-vip-allocator-runtime-extension-tls`
4. Webhook automatically injects CA into `spec.clientConfig.caBundle`
5. ExtensionConfig is created with proper CA ✅

#### Directory Structure
```
examples/extensionconfig-with-ca/
├── kustomization.yaml
├── extensionconfig.yaml
└── README.md (this file)
```

#### Build and Verify

```bash
kustomize build examples/extensionconfig-with-ca/
```

## Alternative: Single GitOps with Dependencies

If you prefer single GitOps repo, use **sync waves** (ArgoCD) or **depends-on** (Fleet):

```yaml
# ArgoCD
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "1"  # Certificate
---
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "2"  # ExtensionConfig
```

```yaml
# Fleet
spec:
  dependsOn:
    - name: capi-vip-allocator-cert
```

## Verification

Check ExtensionConfig has CA:
```bash
kubectl get extensionconfig vip-allocator -n capi-system -o jsonpath='{.spec.clientConfig.caBundle}' | base64 -d
```

Check discovery status:
```bash
kubectl get extensionconfig vip-allocator -n capi-system -o yaml
```

Should show:
```yaml
status:
  conditions:
  - type: Discovered
    status: "True"
    reason: Discovered
```

