package tests

import (
	"context"
	"testing"
	"time"

	"github.com/longbridgeapp/assert"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/errors"
)

func TestGetMultiple(t *testing.T) {
	tests := []struct {
		name       string
		keys       []string
		wantValues map[string]any
		wantErrs   map[string]error
		setup      func(cache *hypercache.HyperCache[backend.InMemory])
	}{
		{
			name:       "get multiple keys with values",
			keys:       []string{"key1", "key2", "key3"},
			wantValues: map[string]any{"key1": 1, "key2": 2, "key3": 3},
			wantErrs:   map[string]error(map[string]error{}),
			setup: func(cache *hypercache.HyperCache[backend.InMemory]) {
				cache.Set(context.TODO(), "key1", 1, 0)
				cache.Set(context.TODO(), "key2", 2, 0)
				cache.Set(context.TODO(), "key3", 3, 0)
			},
		},
		{
			name:       "get multiple keys with missing values",
			keys:       []string{"key1", "key2", "key3"},
			wantValues: map[string]any{"key1": 1, "key3": 3},
			wantErrs:   map[string]error{"key2": errors.ErrKeyNotFound},
			setup: func(cache *hypercache.HyperCache[backend.InMemory]) {
				cache.Set(context.TODO(), "key1", 1, 0)
				cache.Set(context.TODO(), "key3", 3, 0)
			},
		},
		{
			name:       "get multiple keys with expired values",
			keys:       []string{"key1", "key2", "key3"},
			wantValues: map[string]any{"key2": 2, "key3": 3},
			wantErrs:   map[string]error{"key1": errors.ErrKeyNotFound},
			setup: func(cache *hypercache.HyperCache[backend.InMemory]) {
				cache.Set(context.TODO(), "key1", 1, time.Millisecond)
				time.Sleep(2 * time.Millisecond)
				cache.Set(context.TODO(), "key2", 2, 0)
				cache.Set(context.TODO(), "key3", 3, 0)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := &hypercache.Config[backend.InMemory]{
				BackendType: "in-memory",
				HyperCacheOptions: []hypercache.Option[backend.InMemory]{
					hypercache.WithExpirationInterval[backend.InMemory](time.Millisecond),
				},
				InMemoryOptions: []backend.Option[backend.InMemory]{
					backend.WithCapacity[backend.InMemory](10),
				},
			}
			hypercache.GetDefaultManager()
			cache, err := hypercache.New(hypercache.GetDefaultManager(), config)
			assert.Nil(t, err)
			test.setup(cache)

			gotValues, gotErrs := cache.GetMultiple(context.TODO(), test.keys...)
			assert.Equal(t, test.wantValues, gotValues)
			assert.Equal(t, test.wantErrs, gotErrs)
		})
	}
}
