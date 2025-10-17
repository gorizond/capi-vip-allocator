# 🧩 Техническое задание — **CAPI VIP Allocator Operator (без CRD, без label-зависимости)**

> **Project**: Rancher Turtles + CAPI + InCluster IPAM  
> **Component**: `capi-vip-allocator`  
> **Type**: Wrangler Operator (Go)  
> **Status**: 🟢 Draft → MVP  
> **Last Updated**: 2025-10-17  

---

## 📘 Краткое описание

Создать оператор **`capi-vip-allocator`**, который автоматически выделяет IP-адреса для:
1. `Cluster.spec.controlPlaneEndpoint.host`
2. `vip.capi.gorizond.io/ingress` (annotation в `Cluster.metadata.annotations`)

### ❗ Главные принципы:
- ❌ **Не создаём новые CRD**
- ❌ **Не используем метки/аннотации на Cluster для конфигурации**
- ✅ **Всё основано на метках IPAM-пулов** (`ipam.cluster.x-k8s.io`)
- ✅ **Работа с существующими API CAPI и IPAM**
- ✅ **Оператор сам управляет всем жизненным циклом VIP**
- ✅ **Восстанавливает аннотацию ingress-VIP, если её кто-то удалил**

---

## 🎯 Цель

Сделать полностью автономный **allocator**, который:
- Находит IP-пул для `ClusterClass` (по меткам на `GlobalInClusterIPPool`/`InClusterIPPool`)
- Выделяет из пула IP-адрес для control plane и ingress
- Пропатчивает `Cluster.spec.controlPlaneEndpoint.host`
- Проставляет аннотацию `vip.capi.gorizond.io/ingress`
- Отслеживает, чтобы аннотация не исчезала (самовосстановление)
- Высвобождает IP при удалении `Cluster`

---

## 🧱 Архитектура

### Объекты, с которыми работает оператор

| Тип | API | Назначение |
|------|-----|------------|
| `Cluster` | `cluster.x-k8s.io/v1beta2` | целевой объект (где прописывается VIP) |
| `GlobalInClusterIPPool` | `ipam.cluster.x-k8s.io/v1alpha2` | пул IP для control-plane |
| `InClusterIPPool` | `ipam.cluster.x-k8s.io/v1alpha2` | пул IP для ingress (опционально) |
| `IPAddressClaim` | `ipam.cluster.x-k8s.io/v1alpha2` | заявка на IP |
| `IPAddress` | `ipam.cluster.x-k8s.io/v1alpha2` | выделенный IP |

---

## ⚙️ Логика работы

### Контроллер 1: **ClusterControlPlaneVIP**
**Цель:** назначить `Cluster.spec.controlPlaneEndpoint.host`

**Алгоритм:**
1. Отслеживает новые или обновлённые `Cluster`, где:
   - `spec.controlPlaneEndpoint.host` отсутствует  
   - `spec.topology.class` установлен  
2. Определяет `clusterClassName = spec.topology.class`
3. Находит пул:
```yaml
kind: GlobalInClusterIPPool
metadata.labels:
  vip.capi.gorizond.io/cluster-class: <clusterClassName>
  vip.capi.gorizond.io/role: control-plane
````

4. Создаёт `IPAddressClaim`:

```yaml
apiVersion: ipam.cluster.x-k8s.io/v1alpha2
kind: IPAddressClaim
metadata:
  name: vip-cp-<clusterName>
  namespace: <clusterNamespace>
  ownerReferences:
    - apiVersion: cluster.x-k8s.io/v1beta2
      kind: Cluster
      name: <clusterName>
  labels:
    vip.capi.gorizond.io/role: control-plane
spec:
  poolRef:
    apiVersion: ipam.cluster.x-k8s.io/v1alpha2
    kind: GlobalInClusterIPPool
    name: <poolName>
```
5. Ждёт, пока появится связанный `IPAddress`
6. Берёт `IPAddress.spec.address` и:

   * Пишет в `Cluster.spec.controlPlaneEndpoint.host`
   * Устанавливает `port=6443` (если не задан)
7. Проставляет Condition:
   `ControlPlaneVIPReady=True, Reason=IPAddressAssigned`
8. При удалении `Cluster` — удаляет Claim, IP освобождается автоматически.

---

### Контроллер 2: **ClusterIngressVIP**

**Цель:** назначить аннотацию `vip.capi.gorizond.io/ingress` и поддерживать её в актуальном состоянии.

**Алгоритм:**

1. Отслеживает все `Cluster` с `spec.topology.class`
2. Находит пул:

```yaml
kind: GlobalInClusterIPPool
metadata.labels:
  vip.capi.gorizond.io/cluster-class: <clusterClassName>
  vip.capi.gorizond.io/role: ingress
```
3. Если найден — создаёт/проверяет `IPAddressClaim`:

```yaml
apiVersion: ipam.cluster.x-k8s.io/v1alpha2
kind: IPAddressClaim
metadata:
  name: vip-ingress-<clusterName>
  namespace: <clusterNamespace>
  ownerReferences:
    - apiVersion: cluster.x-k8s.io/v1beta2
      kind: Cluster
      name: <clusterName>
  labels:
    vip.capi.gorizond.io/role: ingress
spec:
  poolRef:
    apiVersion: ipam.cluster.x-k8s.io/v1alpha2
    kind: GlobalInClusterIPPool
    name: <poolName>
```
4. Ждёт `IPAddress.status.address`
5. Записывает в аннотацию `vip.capi.gorizond.io/ingress: <address>`
6. Если аннотация была удалена — восстанавливает (self-healing)
7. Condition:
   `IngressVIPReady=True, Reason=IPAddressAssigned`

---

### Поведение при удалении

* `Cluster` удаляется → финализатор оператора:

  * Удаляет оба `IPAddressClaim`
  * Логику освобождения IP обеспечивает IPAM provider
* Conditions сбрасываются в `False`

---

## 🧩 Пример IPAM пулов

```yaml
apiVersion: ipam.cluster.x-k8s.io/v1alpha2
kind: GlobalInClusterIPPool
metadata:
  name: vip-cp-pool
  labels:
    vip.capi.gorizond.io/cluster-class: rke2-cluster-class
    vip.capi.gorizond.io/role: control-plane
spec:
  addresses:
    - 10.0.0.10-10.0.0.20
  prefix: 24
---
apiVersion: ipam.cluster.x-k8s.io/v1alpha2
kind: GlobalInClusterIPPool
metadata:
  name: vip-ingress-pool
  labels:
    vip.capi.gorizond.io/cluster-class: rke2-cluster-class
    vip.capi.gorizond.io/role: ingress
spec:
  addresses:
    - 10.0.1.10-10.0.1.50
  prefix: 24
```

---

## 🧠 Условия приёмки (Acceptance Criteria)

| № | Критерий                 | Описание                                                                                     |
| - | ------------------------ | -------------------------------------------------------------------------------------------- |
| 1 | ControlPlane VIP         | После создания кластера в течение 2 мин `spec.controlPlaneEndpoint.host` заполнен IP из пула |
| 2 | Ingress VIP              | В `metadata.annotations.vip.capi.gorizond.io/ingress` записан IP из ingress-пула             |
| 3 | Восстановление аннотации | При удалении аннотации оператор восстанавливает её в течение 30 сек                          |
| 4 | Удаление кластера        | IP освобождается (Claims удалены, IP возвращён в пул)                                        |
| 5 | Idempotency              | Повторные reconcile не создают дубликаты ресурсов                                            |
| 6 | Zero CRD                 | Оператор не добавляет новые API-объекты                                                      |

---

## ⚙️ RBAC

* **read**:

  * `clusters.cluster.x-k8s.io`
  * `globalinclusterippools.ipam.cluster.x-k8s.io`
  * `ipaddresses.ipam.cluster.x-k8s.io`
* **write**:

  * `ipaddressclaims.ipam.cluster.x-k8s.io`
  * `clusters.cluster.x-k8s.io/status` (patch only)
* **finalizer management**: `update` on `clusters`

---

## 📦 Установка через Rancher Turtles

```yaml
apiVersion: turtles-capi.cattle.io/v1alpha1
kind: CAPIProvider
metadata:
  name: capi-vip-allocator
  namespace: capi-system
spec:
  fetchConfig:
    url: https://github.com/gorizond/capi-vip-allocator/releases/download/v0.0.0/capi-vip-allocator.yaml
  name: capi-vip-allocator
  type: addon
  version: v0.0.0
```

**Компоненты чарта:**

* Deployment `capi-vip-allocator`
* SA + RBAC
* ConfigMap (опции: retryInterval, reconcilePeriod, defaultPort)
* Service (метрики)
* ServiceMonitor (опционально)

---

## 🧩 Helm values (пример)

```yaml
controller:
  reconcilePeriod: 30s
  retryInterval: 10s
defaults:
  port: 6443
  baseDomain: clusters.internal
image:
  repository: ghcr.io/gorizond/capi-vip-allocator
  tag: v0.0.0
metrics:
  enabled: true
rbac:
  create: true
```

---

## 🧪 Тестирование

### Unit

* Резолв пула по `clusterClass`
* Создание Claims, ожидание IP
* Патч spec/controlPlaneEndpoint
* Аннотация ingress-VIP и восстановление

### E2E (Kind)

1. Установить CAPI + IPAM + оператор
2. Создать пул для control-plane и ingress
3. Создать Cluster (topology class задан, endpoint пуст)
4. Проверить:

   * `Cluster.spec.controlPlaneEndpoint.host` заполнен
   * `Cluster.metadata.annotations.vip.capi.gorizond.io/ingress` существует
5. Удалить Cluster → проверить, что IP освобождён

---

## 📊 Метрики и события

| Метрика                                         | Описание                    |                                |
| ----------------------------------------------- | --------------------------- | ------------------------------ |
| `capi_vip_allocate_total{role="control-plane или ingress"}`                  | количество успешных назначений |
| `capi_vip_reconcile_failures_total{reason=...}` | ошибки reconcile            |                                |
| `capi_vip_annotation_restore_total`             | восстановленные аннотации   |                                |
| `capi_vip_ip_wait_seconds_bucket`               | время ожидания выделения IP |                                |

**Events на Cluster:**

* `VIPAllocated`
* `IngressAnnotationSet`
* `AnnotationRestored`
* `VIPReleased`

---

## 🔄 Алгоритм (псевдокод)

```go
// Cluster Reconcile
if cluster.Spec.Topology == nil {
  return
}

if cluster.Spec.ControlPlaneEndpoint.Host == "" {
  pool := findPool(cluster.Spec.Topology.Class, "control-plane")
  claim := ensureClaim(cluster, pool, "control-plane")
  ip := waitForIPAddress(claim)
  patchClusterEndpoint(cluster, ip, port=defaults.port)
}

poolIngress := findPool(cluster.Spec.Topology.Class, "ingress")
if poolIngress != nil {
  claimIngress := ensureClaim(cluster, poolIngress, "ingress")
  ipIngress := waitForIPAddress(claimIngress)
  ensureIngressAnnotation(cluster, ipIngress)
}
```

---

## 🧭 Roadmap

| Этап | Цель                             | Статус     |
| ---- | -------------------------------- | ---------- |
| 1    | Control-plane VIP allocation     | 🟢 MVP     |
| 2    | Ingress VIP + annotation restore | 🟡 Next    |
| 3    | Prometheus metrics + Conditions  | ⚙️ Planned |
| 4    | Multi-namespace IP pools support | ⚙️ Future  |
| 5    | Runtime Extension / Webhook      | ⚪ Later    |

---

## ✅ Резюме

Оператор `capi-vip-allocator`:

* Работает с существующими объектами CAPI и IPAM
* Не требует ручных меток или CRD
* Автоматически:

  * выделяет IP для control plane и ingress
  * прописывает `Cluster.spec.controlPlaneEndpoint.host`
  * добавляет аннотацию `vip.capi.gorizond.io/ingress`
  * восстанавливает аннотацию при её удалении
  * освобождает IP при удалении кластера
* Простая установка через Rancher Turtles как `addon`-провайдер
* 100 % совместим с Rancher + Cluster API + IPAM v1alpha2

