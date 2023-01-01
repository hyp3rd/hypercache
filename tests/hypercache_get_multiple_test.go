package tests

import (
	"reflect"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/longbridgeapp/assert"
)

func TestGetMultiple(t *testing.T) {
	tests := []struct {
		name       string
		keys       []string
		wantValues map[string]interface{}
		wantErrs   []error
		setup      func(*hypercache.HyperCache)
	}{
		{
			name:       "get multiple keys with values",
			keys:       []string{"key1", "key2", "key3"},
			wantValues: map[string]interface{}{"key1": 1, "key2": 2, "key3": 3},
			wantErrs:   []error(nil),
			setup: func(cache *hypercache.HyperCache) {
				cache.Set("key1", 1, 0)
				cache.Set("key2", 2, 0)
				cache.Set("key3", 3, 0)
			},
		},
		{
			name:       "get multiple keys with missing values",
			keys:       []string{"key1", "key2", "key3"},
			wantValues: map[string]interface{}{"key1": 1, "key3": 3},
			wantErrs:   []error{hypercache.ErrKeyNotFound},
			setup: func(cache *hypercache.HyperCache) {
				cache.Set("key1", 1, 0)
				cache.Set("key3", 3, 0)
			},
		},
		{
			name:       "get multiple keys with expired values",
			keys:       []string{"key1", "key2", "key3"},
			wantValues: map[string]interface{}{"key2": 2, "key3": 3},
			wantErrs:   []error{hypercache.ErrKeyNotFound},
			setup: func(cache *hypercache.HyperCache) {
				cache.Set("key1", 1, time.Millisecond)
				time.Sleep(2 * time.Millisecond)
				cache.Set("key2", 2, 0)
				cache.Set("key3", 3, 0)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache, err := hypercache.NewHyperCache(10, hypercache.WithExpirationInterval(time.Millisecond))
			assert.Nil(t, err)
			test.setup(cache)

			gotValues, gotErrs := cache.GetMultiple(test.keys...)
			assert.Equal(t, test.wantValues, gotValues)
			assert.Equal(t, test.wantErrs, gotErrs)
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache, err := hypercache.NewHyperCache(10, hypercache.WithExpirationInterval(time.Millisecond))
			assert.Nil(t, err)
			test.setup(cache)

			got, gotErr := cache.GetMultiple(test.keys...)
			if !reflect.DeepEqual(got, test.wantValues) {
				t.Errorf("got %v, want %v", got, test.wantValues)
				t.Errorf("got %v, want %v", gotErr, test.wantErrs)
			}
		})
	}
}
