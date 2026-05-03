package constants

const (
	// ErrorLabel is a common label for all errors in HyperCache.
	ErrorLabel = "error"
	// ErrMsgMissingCacheKey is returned when cache key is missing in request.
	ErrMsgMissingCacheKey = "missing cache key"
	// ErrMsgUnsupportedDistributedBackend is returned when the distributed backend does not support the requested operation.
	ErrMsgUnsupportedDistributedBackend = "distributed backend unsupported"
)
