package tests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/sentinel"
)

// Test fixture keys/values shared across the table-driven Set/Get/GetOrSet/
// GetMultiple tests in this package. Extracted as constants to satisfy
// `goconst` and to reduce typo risk when adding new subtests.
const (
	testKey1   = "key1"
	testKey2   = "key2"
	testKey3   = "key3"
	testValue1 = "value1"
	testValue2 = "value2"
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
			key:           testKey1,
			value:         testValue1,
			expiry:        0,
			expectedValue: testValue1,
			expectedErr:   nil,
		},
		{
			name:          "set with valid key and value with expiry",
			key:           testKey2,
			value:         testValue2,
			expiry:        time.Second,
			expectedValue: testValue2,
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
			key:           testKey1,
			value:         "new_value1",
			expiry:        0,
			expectedValue: "new_value1",
			expectedErr:   nil,
		},
		{
			name:          "update expiry of existing key",
			key:           testKey1,
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
