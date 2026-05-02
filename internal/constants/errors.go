package constants

const (
	// ErrorLabel is a common label for all errors in HyperCache.
	ErrorLabel = "error"
	// ErrMegMissingCacheKey is returned when cache key is missing in request.
	ErrMegMissingCacheKey = "missing cache key"
	// ErrMsgUnsupportedDistributedBackend is returned when the distributed backend does not support the requested operation.
	ErrMsgUnsupportedDistributedBackend = "distributed backend unsupported"
)
