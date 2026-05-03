package hypercache

import (
	"context"
	"runtime"
	"time"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
	"github.com/hyp3rd/hypercache/pkg/stats"
)

// resolveBackend constructs a typed backend instance based on the config.BackendType.
func resolveBackend[T backend.IBackendConstrain](ctx context.Context, bm *BackendManager, config *Config[T]) (backend.IBackend[T], error) {
	constructor, exists := bm.backendRegistry[config.BackendType]
	if !exists {
		return nil, ewrap.Newf("unknown backend type: %s", config.BackendType)
	}

	switch config.BackendType {
	case constants.InMemoryBackend:
		return resolveInMemoryBackend[T](ctx, constructor, config)
	case constants.RedisBackend:
		return resolveRedisBackend[T](ctx, constructor, config)
	case constants.RedisClusterBackend:
		return resolveRedisClusterBackend[T](ctx, constructor, config)
	case constants.DistMemoryBackend:
		return resolveDistMemoryBackend[T](ctx, constructor, config)
	default:
		return nil, ewrap.Newf("unknown backend type: %s", config.BackendType)
	}
}

// castBackend tries to cast a backend instance of any concrete type to backend.IBackend[T].
func castBackend[T backend.IBackendConstrain](bi any) (backend.IBackend[T], error) {
	if b, ok := bi.(backend.IBackend[T]); ok {
		return b, nil
	}

	return nil, sentinel.ErrInvalidBackendType
}

func resolveInMemoryBackend[T backend.IBackendConstrain](ctx context.Context, constructor, cfgAny any) (backend.IBackend[T], error) {
	inMemCtor, ok := constructor.(InMemoryBackendConstructor)
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	cfg, ok := cfgAny.(*Config[backend.InMemory])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	bi, err := inMemCtor.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return castBackend[T](bi)
}

func resolveRedisBackend[T backend.IBackendConstrain](ctx context.Context, constructor, cfgAny any) (backend.IBackend[T], error) {
	redisCtor, ok := constructor.(RedisBackendConstructor)
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	cfg, ok := cfgAny.(*Config[backend.Redis])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	bi, err := redisCtor.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return castBackend[T](bi)
}

func resolveDistMemoryBackend[T backend.IBackendConstrain](ctx context.Context, constructor, cfgAny any) (backend.IBackend[T], error) {
	distCtor, ok := constructor.(DistMemoryBackendConstructor)
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	cfg, ok := cfgAny.(*Config[backend.DistMemory])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	bi, err := distCtor.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return castBackend[T](bi)
}

func resolveRedisClusterBackend[T backend.IBackendConstrain](ctx context.Context, constructor, cfgAny any) (backend.IBackend[T], error) {
	clusterCtor, ok := constructor.(RedisClusterBackendConstructor)
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	cfg, ok := cfgAny.(*Config[backend.RedisCluster])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	bi, err := clusterCtor.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return castBackend[T](bi)
}

// newHyperCacheBase builds the base HyperCache instance with default timings and internals.
func newHyperCacheBase[T backend.IBackendConstrain](b backend.IBackend[T]) *HyperCache[T] {
	return &HyperCache[T]{
		backend:            b,
		itemPoolManager:    cache.NewItemPoolManager(),
		workerPool:         NewWorkerPool(runtime.NumCPU()),
		stop:               make(chan bool, 2),
		evictCh:            make(chan bool, 1),
		expirationInterval: constants.DefaultExpirationInterval,
		evictionInterval:   constants.DefaultEvictionInterval,
		// Default eviction shard count matches pkg/cache/v2.ShardCount so a
		// key's data shard and eviction shard map to the same logical position.
		// Users can override with WithEvictionShardCount; <=1 disables sharding.
		evictionShardCount: cache.ShardCount,
	}
}

// configureStats sets the stats collector, using default if none specified.
func configureStats[T backend.IBackendConstrain](hc *HyperCache[T]) error {
	if hc.statsCollectorName == "" {
		hc.StatsCollector = stats.NewHistogramStatsCollector()

		return nil
	}

	var err error

	hc.StatsCollector, err = stats.NewCollector(hc.statsCollectorName)

	return err
}

// checkCapacity validates the backend capacity and returns an error if invalid.
func checkCapacity[T backend.IBackendConstrain](hc *HyperCache[T]) error {
	if hc.backend.Capacity() < 0 {
		return sentinel.ErrInvalidCapacity
	}

	return nil
}

// initExpirationTrigger initializes the expiration trigger channel with optional buffer override.
func initExpirationTrigger[T backend.IBackendConstrain](hc *HyperCache[T]) {
	buf := hc.backend.Capacity() / 2
	if hc.expirationTriggerBufSize > 0 {
		buf = hc.expirationTriggerBufSize
	}

	if buf < 1 {
		buf = 1
	}

	hc.expirationTriggerCh = make(chan bool, buf)
}

// startBackgroundJobs starts the expiration and eviction loops once.
// Both loops are tied to a long-lived background context so Stop() can
// cancel them deterministically — independent of which side of the stop
// channel races first.
func (hyperCache *HyperCache[T]) startBackgroundJobs(ctx context.Context) {
	hyperCache.once.Do(func() {
		// Long-lived background context, canceled on Stop.
		jobsCtx, cancel := context.WithCancel(ctx)

		hyperCache.bgCancel = cancel
		// Ensure shutdown signaling always drives context cancellation, even when
		// stop consumers race to read the stop channel.
		go func(stop <-chan bool, done <-chan struct{}, cancel context.CancelFunc) {
			select {
			case <-stop:
				cancel()
			case <-done:
			}
		}(hyperCache.stop, jobsCtx.Done(), cancel)

		hyperCache.startExpirationRoutine(jobsCtx)
		hyperCache.startEvictionRoutine(jobsCtx)
	})
}

// shutdownTimeout caps the wait for graceful shutdown of optional management
// HTTP servers in Stop. Kept short — Stop is meant to be quick and tests
// don't have minutes to spare.
const shutdownTimeout = 2 * time.Second
