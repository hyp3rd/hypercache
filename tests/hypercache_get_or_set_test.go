package tests

import (
	"context"
	"testing"
	"time"

	"github.com/longbridgeapp/assert"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/errors"
)

func TestHyperCache_GetOrSet(t *testing.T) {
	tests := []struct {
		name          string
		key           string
		value         any
		expiry        time.Duration
		expectedValue any
		expectedErr   error
	}{
		{
			name:          "get or set with valid key and value",
			key:           "key1",
			value:         "value1",
			expiry:        0,
			expectedValue: "value1",
			expectedErr:   nil,
		},
		{
			name:          "get or set with valid key and value with expiry",
			key:           "key2",
			value:         "value2",
			expiry:        time.Second,
			expectedValue: "value2",
			expectedErr:   nil,
		},
		// {
		// 	name:          "get or set with empty key",
		// 	key:           "",
		// 	value:         "value3",
		// 	expiry:        0,
		// 	expectedValue: nil,
		// 	expectedErr:   hypercache.ErrInvalidKey,
		// },
		{
			name:          "get or set with nil value",
			key:           "key4",
			value:         nil,
			expiry:        0,
			expectedValue: nil,
			expectedErr:   errors.ErrNilValue,
		},
		{
			name:          "get or set with key that has expired",
			key:           "key5",
			value:         "value5",
			expiry:        time.Millisecond,
			expectedValue: nil,
			expectedErr:   errors.ErrKeyExpired,
		},
		{
			name:          "get or set with key that already exists",
			key:           "key1",
			value:         "value6",
			expiry:        0,
			expectedValue: "value1",
			expectedErr:   nil,
		},
	}
	cache, err := hypercache.NewInMemoryWithDefaults(10)
	assert.Nil(t, err)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				val any
				err error
			)

			shouldExpire := test.expectedErr == errors.ErrKeyExpired

			val, err = cache.GetOrSet(context.TODO(), test.key, test.value, test.expiry)
			if !shouldExpire {
				assert.Equal(t, test.expectedErr, err)
			}

			if err == nil && !shouldExpire {
				assert.Equal(t, test.expectedValue, val)
			}

			if shouldExpire {
				t.Log("sleeping for 2 Millisecond to allow the key to expire")
				time.Sleep(2 * time.Millisecond)
				_, err = cache.GetOrSet(context.TODO(), test.key, test.value, test.expiry)
				assert.Equal(t, test.expectedErr, err)

			}

			// Check if the value has been set in the cache
			if err == nil {
				val, ok := cache.Get(test.key)
				assert.True(t, ok)
				assert.Equal(t, test.expectedValue, val)
			} else {
				val, ok := cache.Get(test.key)
				assert.False(t, ok)
				assert.Nil(t, val)
			}
		})
	}
}
