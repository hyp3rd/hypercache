// Package attrs provides reusable OpenTelemetry attribute key constants
// to avoid duplication across middlewares.
// Package attrs defines telemetry attribute keys used for observability and monitoring
// across the hypercache system. These constants provide standardized key names for
// metrics, traces, and logs to ensure consistent telemetry data collection.
package attrs

const (
	// AttrKeyLength represents the telemetry attribute key for measuring the length
	// of a cache key in bytes. This metric helps monitor key size distribution
	// and identify potential performance impacts from oversized keys.
	AttrKeyLength = "key.len"
	// AttrKeysCount represents the telemetry attribute key for measuring the number
	// of cache keys being processed. This metric helps monitor the workload and
	// identify potential bottlenecks in key management.
	AttrKeysCount = "keys.count"
	// AttrResultCount represents the telemetry attribute key for measuring the number
	// of cache results returned. This metric helps monitor the effectiveness of cache
	// lookups and identify potential issues with cache population.
	AttrResultCount = "result.count"
	// AttrFailedCount represents the telemetry attribute key for measuring the number
	// of cache operations that failed. This metric helps monitor error rates and
	// identify potential issues with cache reliability.
	AttrFailedCount = "failed.count"
	// AttrExpirationMS represents the telemetry attribute key for measuring the expiration
	// time of cache items in milliseconds. This metric helps monitor cache item lifetimes
	// and identify potential issues with cache eviction policies.
	AttrExpirationMS = "expiration.ms"
)
