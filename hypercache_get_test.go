package hypercache

import (
	"testing"
	"time"

	"github.com/longbridgeapp/assert"
)

func TestHyperCache_Get(t *testing.T) {
	tests := []struct {
		name          string
		key           string
		value         interface{}
		expiry        time.Duration
		expectedValue interface{}
		expectedErr   error
		sleep         time.Duration
		shouldSet     bool
	}{
		{
			name:          "get with valid key",
			key:           "key1",
			value:         "value1",
			expiry:        0,
			expectedValue: "value1",
			expectedErr:   nil,
		},
		{
			name:          "get with valid key and value with expiry",
			key:           "key2",
			value:         "value2",
			expiry:        5 * time.Second,
			expectedValue: "value2",
			expectedErr:   nil,
		},
		{
			name:          "get with empty key",
			key:           "",
			value:         "value3",
			expiry:        0,
			expectedValue: "",
			expectedErr:   ErrInvalidKey,
		},
		{
			name:          "get with expired key",
			key:           "key4",
			value:         "value4",
			expiry:        1 * time.Second,
			expectedValue: nil,
			expectedErr:   nil,
			sleep:         2 * time.Second,
		},
		{
			name:          "get with non-existent key",
			key:           "key5",
			value:         "value5",
			expiry:        0,
			expectedValue: nil,
			expectedErr:   ErrKeyNotFound,
			shouldSet:     false,
		},
	}
	cache, err := NewHyperCache(10)
	assert.Nil(t, err)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.shouldSet {
				err = cache.Set(test.key, test.value, test.expiry)
				if err != nil {
					assert.Equal(t, test.expectedErr, err)
				}

				if test.sleep > 0 {
					time.Sleep(test.sleep)
				}
			}

			val, ok := cache.Get(test.key)
			if test.expectedErr != nil || !ok {
				assert.False(t, ok)
			} else {
				assert.True(t, ok)
				assert.Equal(t, test.expectedValue, val)
			}
		})
	}
}
