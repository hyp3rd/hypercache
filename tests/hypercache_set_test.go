package tests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/sentinel"
)

func TestHyperCache_Set(t *testing.T) { //nolint:paralleltest // subtests share cache instance and depend on insertion order
	tests := []struct {
		name          string
		key           string
		value         any
		expiry        time.Duration
		expectedValue any
		expectedErr   error
	}{
		{
			name:          "set with valid key and value",
			key:           "key1",
			value:         "value1",
			expiry:        0,
			expectedValue: "value1",
			expectedErr:   nil,
		},
		{
			name:          "set with valid key and value with expiry",
			key:           "key2",
			value:         "value2",
			expiry:        time.Second,
			expectedValue: "value2",
			expectedErr:   nil,
		},
		{
			name:          "set with empty key",
			key:           "",
			value:         "value3",
			expiry:        0,
			expectedValue: nil,
			expectedErr:   sentinel.ErrInvalidKey,
		},
		{
			name:          "set with nil value",
			key:           "key4",
			value:         nil,
			expiry:        0,
			expectedValue: nil,
			expectedErr:   sentinel.ErrNilValue,
		},
		{
			name:          "overwrite existing key",
			key:           "key1",
			value:         "new_value1",
			expiry:        0,
			expectedValue: "new_value1",
			expectedErr:   nil,
		},
		{
			name:          "update expiry of existing key",
			key:           "key1",
			value:         "new_value1",
			expiry:        time.Second,
			expectedValue: "new_value1",
			expectedErr:   nil,
		},
	}
	cache, err := hypercache.NewInMemoryWithDefaults(context.TODO(), 10)
	require.NoError(t, err)

	t.Cleanup(func() { _ = cache.Stop(context.TODO()) })

	for _, test := range tests { //nolint:paralleltest // subtests share cache instance
		t.Run(test.name, func(t *testing.T) {
			err = cache.Set(context.TODO(), test.key, test.value, test.expiry)
			require.Equal(t, test.expectedErr, err)

			if err == nil {
				val, ok := cache.Get(context.TODO(), test.key)
				require.True(t, ok)
				require.Equal(t, test.expectedValue, val)
			}
		})
	}
}
