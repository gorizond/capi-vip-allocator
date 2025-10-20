package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// VipAllocationsTotal tracks the total number of VIP allocations by role
	VipAllocationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "capi_vip_allocator_allocations_total",
			Help: "Total number of VIP allocations by role",
		},
		[]string{"role", "cluster_class"},
	)

	// VipAllocationErrorsTotal tracks VIP allocation errors
	VipAllocationErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "capi_vip_allocator_allocation_errors_total",
			Help: "Total number of VIP allocation errors",
		},
		[]string{"role", "cluster_class", "reason"},
	)

	// VipAllocationDurationSeconds tracks VIP allocation duration
	VipAllocationDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "capi_vip_allocator_allocation_duration_seconds",
			Help:    "Duration of VIP allocation operations in seconds",
			Buckets: prometheus.DefBuckets, // 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10
		},
		[]string{"role", "cluster_class"},
	)

	// VipPoolsAvailable tracks the number of available IP pools
	VipPoolsAvailable = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capi_vip_allocator_pools_available",
			Help: "Number of available GlobalInClusterIPPools by cluster class and role",
		},
		[]string{"cluster_class", "role"},
	)

	// VipPoolAddressesTotal tracks total addresses in pool
	VipPoolAddressesTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capi_vip_allocator_pool_addresses_total",
			Help: "Total number of IP addresses in pool",
		},
		[]string{"pool_name"},
	)

	// VipPoolAddressesFree tracks free addresses in pool
	VipPoolAddressesFree = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capi_vip_allocator_pool_addresses_free",
			Help: "Number of free IP addresses in pool",
		},
		[]string{"pool_name"},
	)

	// VipPoolAddressesUsed tracks used addresses in pool
	VipPoolAddressesUsed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capi_vip_allocator_pool_addresses_used",
			Help: "Number of used IP addresses in pool",
		},
		[]string{"pool_name"},
	)

	// VipClaimsTotal tracks active IPAddressClaims
	VipClaimsTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capi_vip_allocator_claims_total",
			Help: "Total number of active IPAddressClaims",
		},
		[]string{"role", "namespace"},
	)

	// VipClaimsReady tracks ready IPAddressClaims
	VipClaimsReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capi_vip_allocator_claims_ready",
			Help: "Number of IPAddressClaims with allocated IP",
		},
		[]string{"role", "namespace"},
	)

	// VipClaimsPending tracks pending IPAddressClaims
	VipClaimsPending = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "capi_vip_allocator_claims_pending",
			Help: "Number of IPAddressClaims waiting for IP allocation",
		},
		[]string{"role", "namespace"},
	)

	// VipReconcileTotal tracks controller reconcile operations
	VipReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "capi_vip_allocator_reconcile_total",
			Help: "Total number of cluster reconcile operations",
		},
		[]string{"cluster_class", "result"},
	)

	// VipReconcileDurationSeconds tracks reconcile duration
	VipReconcileDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "capi_vip_allocator_reconcile_duration_seconds",
			Help:    "Duration of cluster reconcile operations in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"cluster_class"},
	)
)

func init() {
	// Register metrics with controller-runtime metrics registry
	metrics.Registry.MustRegister(
		VipAllocationsTotal,
		VipAllocationErrorsTotal,
		VipAllocationDurationSeconds,
		VipPoolsAvailable,
		VipPoolAddressesTotal,
		VipPoolAddressesFree,
		VipPoolAddressesUsed,
		VipClaimsTotal,
		VipClaimsReady,
		VipClaimsPending,
		VipReconcileTotal,
		VipReconcileDurationSeconds,
	)
}

