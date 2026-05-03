package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

type hyperCacheGetOrSetCase struct {
	name          string
	key           string
	value         any
	expiry        time.Duration
	expectedValue any
	expectedErr   error
}

func runGetOrSetCase(t *testing.T, cache *hypercache.HyperCache[backend.InMemory], tc hyperCacheGetOrSetCase) {
	t.Helper()

	shouldExpire := errors.Is(tc.expectedErr, sentinel.ErrKeyExpired)

	val, err := cache.GetOrSet(context.TODO(), tc.key, tc.value, tc.expiry)
	if !shouldExpire {
		require.Equal(t, tc.expectedErr, err)
	}

	if err == nil && !shouldExpire {
		require.Equal(t, tc.expectedValue, val)
	}

	if shouldExpire {
		t.Log("sleeping for 2 Millisecond to allow the key to expire")
		time.Sleep(2 * time.Millisecond)

		_, err = cache.GetOrSet(context.TODO(), tc.key, tc.value, tc.expiry)
		require.Equal(t, tc.expectedErr, err)
	}

	gotVal, ok := cache.Get(context.TODO(), tc.key)

	if err == nil {
		require.True(t, ok)
		require.Equal(t, tc.expectedValue, gotVal)

		return
	}

	require.False(t, ok)
	require.Nil(t, gotVal)
}

func TestHyperCache_GetOrSet(t *testing.T) { //nolint:paralleltest // subtests share cache instance and depend on insertion order
	tests := []hyperCacheGetOrSetCase{
		{
			name:          "get or set with valid key and value",
			key:           testKey1,
			value:         testValue1,
			expectedValue: testValue1,
		},
		{
			name:          "get or set with valid key and value with expiry",
			key:           testKey2,
			value:         testValue2,
			expiry:        time.Second,
			expectedValue: testValue2,
		},
		{
			name:        "get or set with nil value",
			key:         "key4",
			value:       nil,
			expectedErr: sentinel.ErrNilValue,
		},
		{
			name:        "get or set with key that has expired",
			key:         "key5",
			value:       "value5",
			expiry:      time.Millisecond,
			expectedErr: sentinel.ErrKeyExpired,
		},
		{
			name:          "get or set with key that already exists",
			key:           testKey1,
			value:         "value6",
			expectedValue: testValue1,
		},
	}

	cache, err := hypercache.NewInMemoryWithDefaults(context.TODO(), 10)
	require.NoError(t, err)

	for _, tc := range tests { //nolint:paralleltest // subtests share cache instance
		t.Run(tc.name, func(t *testing.T) {
			runGetOrSetCase(t, cache, tc)
		})
	}
}
