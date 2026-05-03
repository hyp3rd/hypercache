package tests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

func TestGetMultiple(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		keys       []string
		wantValues map[string]any
		wantErrs   map[string]error
		setup      func(cache *hypercache.HyperCache[backend.InMemory])
	}{
		{
			name:       "get multiple keys with values",
			keys:       []string{testKey1, testKey2, testKey3},
			wantValues: map[string]any{testKey1: 1, testKey2: 2, testKey3: 3},
			wantErrs:   map[string]error{},
			setup: func(cache *hypercache.HyperCache[backend.InMemory]) {
				_ = cache.Set(context.TODO(), testKey1, 1, 0)
				_ = cache.Set(context.TODO(), testKey2, 2, 0)
				_ = cache.Set(context.TODO(), testKey3, 3, 0)
			},
		},
		{
			name:       "get multiple keys with missing values",
			keys:       []string{testKey1, testKey2, testKey3},
			wantValues: map[string]any{testKey1: 1, testKey3: 3},
			wantErrs:   map[string]error{testKey2: sentinel.ErrKeyNotFound},
			setup: func(cache *hypercache.HyperCache[backend.InMemory]) {
				_ = cache.Set(context.TODO(), testKey1, 1, 0)
				_ = cache.Set(context.TODO(), testKey3, 3, 0)
			},
		},
		{
			name:       "get multiple keys with expired values",
			keys:       []string{testKey1, testKey2, testKey3},
			wantValues: map[string]any{testKey2: 2, testKey3: 3},
			wantErrs:   map[string]error{testKey1: sentinel.ErrKeyNotFound},
			setup: func(cache *hypercache.HyperCache[backend.InMemory]) {
				_ = cache.Set(context.TODO(), testKey1, 1, time.Millisecond)
				time.Sleep(2 * time.Millisecond)

				_ = cache.Set(context.TODO(), testKey2, 2, 0)
				_ = cache.Set(context.TODO(), testKey3, 3, 0)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := &hypercache.Config[backend.InMemory]{
				BackendType: constants.InMemoryBackend,
				HyperCacheOptions: []hypercache.Option[backend.InMemory]{
					hypercache.WithExpirationInterval[backend.InMemory](time.Millisecond),
				},
				InMemoryOptions: []backend.Option[backend.InMemory]{
					backend.WithCapacity[backend.InMemory](10),
				},
			}

			hypercache.GetDefaultManager()

			cache, err := hypercache.New(context.TODO(), hypercache.GetDefaultManager(), config)
			require.NoError(t, err)
			test.setup(cache)

			gotValues, gotErrs := cache.GetMultiple(context.TODO(), test.keys...)
			require.Equal(t, test.wantValues, gotValues)
			require.Equal(t, test.wantErrs, gotErrs)
		})
	}
}
