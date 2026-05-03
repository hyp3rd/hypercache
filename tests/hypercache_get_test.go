package tests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

type hyperCacheGetCase struct {
	name          string
	key           string
	value         any
	expiry        time.Duration
	expectedValue any
	expectedErr   error
	sleep         time.Duration
	shouldSet     bool
}

func runHyperCacheGetCase(t *testing.T, cache *hypercache.HyperCache[backend.InMemory], tc hyperCacheGetCase) {
	t.Helper()

	if tc.shouldSet {
		err := cache.Set(context.TODO(), tc.key, tc.value, tc.expiry)
		if err != nil {
			require.Equal(t, tc.expectedErr, err)
		}

		if tc.sleep > 0 {
			time.Sleep(tc.sleep)
		}
	}

	val, ok := cache.Get(context.TODO(), tc.key)
	if tc.expectedErr != nil || !ok {
		require.False(t, ok)

		return
	}

	require.True(t, ok)
	require.Equal(t, tc.expectedValue, val)
}

func TestHyperCache_Get(t *testing.T) { //nolint:paralleltest // subtests share cache instance
	tests := []hyperCacheGetCase{
		{
			name:          "get with valid key",
			key:           testKey1,
			value:         testValue1,
			expiry:        0,
			expectedValue: testValue1,
		},
		{
			name:          "get with valid key and value with expiry",
			key:           testKey2,
			value:         testValue2,
			expiry:        5 * time.Second,
			expectedValue: testValue2,
		},
		{
			name:   "get with expired key",
			key:    "key4",
			value:  "value4",
			expiry: 1 * time.Second,
			sleep:  2 * time.Second,
		},
		{
			name:        "get with non-existent key",
			key:         "key5",
			value:       "value5",
			expectedErr: sentinel.ErrKeyNotFound,
		},
	}

	cache, err := hypercache.NewInMemoryWithDefaults(context.TODO(), 10)
	require.NoError(t, err)

	for _, tc := range tests { //nolint:paralleltest // subtests share cache instance
		t.Run(tc.name, func(t *testing.T) {
			runHyperCacheGetCase(t, cache, tc)
		})
	}
}
