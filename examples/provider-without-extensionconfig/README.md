# CAPI VIP Allocator Provider - Without ExtensionConfig

This directory contains instructions for deploying **only** the provider components without ExtensionConfig.

## Use Case

For **two-stage GitOps deployment**:
- **Stage 1** (this): Deploy provider core (Certificate, Deployment, Services, RBAC)
- **Stage 2**: Deploy ExtensionConfig separately (see `../extensionconfig-with-ca/`)

## Why Separate?

ExtensionConfig needs `caBundle` injected by CAPI controller-manager webhook. This only works reliably when:
1. Certificate is already Ready
2. Secret with CA certificate exists
3. ExtensionConfig is created AFTER the above

## Generate Manifest

To generate provider manifest WITHOUT ExtensionConfig:

```bash
# Build full manifest
make release-manifests TAG=v0.1.0

# Remove ExtensionConfig section
sed -i.bak '/^---$/,/^kind: ExtensionConfig$/d' out/capi-vip-allocator.yaml
# or use yq
yq eval 'select(.kind != "ExtensionConfig")' out/capi-vip-allocator.yaml > out/capi-vip-allocator-no-extconfig.yaml
```

## Deploy via CAPIProvider

```yaml
apiVersion: turtles-capi.cattle.io/v1alpha1
kind: CAPIProvider
metadata:
  name: capi-vip-allocator
  namespace: capi-system
spec:
  name: vip-allocator
  type: addon
  version: v0.1.0
  fetchConfig:
    url: https://github.com/gorizond/capi-vip-allocator/releases/download/v0.1.0/capi-vip-allocator-no-extconfig.yaml
```

## After Deployment

1. Verify Certificate is Ready:
   ```bash
   kubectl get certificate -n capi-system capi-vip-allocator-runtime-extension-cert
   ```

2. Verify Secret exists with CA:
   ```bash
   kubectl get secret -n capi-system capi-vip-allocator-runtime-extension-tls -o jsonpath='{.data.ca\.crt}' | base64 -d
   ```

3. Then deploy ExtensionConfig from Stage 2

## Alternative: Use Fleet dependsOn

```yaml
# Stage 1 - Provider
apiVersion: fleet.cattle.io/v1alpha1
kind: GitRepo
metadata:
  name: capi-vip-allocator-provider
  namespace: fleet-default
spec:
  repo: https://github.com/gorizond/capi-vip-allocator
  branch: main
  paths:
    - examples/provider-without-extensionconfig

---
# Stage 2 - ExtensionConfig
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
  dependsOn:
    - name: capi-vip-allocator-provider
      namespace: fleet-default
```

