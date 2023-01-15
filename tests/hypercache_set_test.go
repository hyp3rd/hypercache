package tests

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/longbridgeapp/assert"
)

func TestHyperCache_Set(t *testing.T) {
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
			expectedErr:   errors.ErrInvalidKey,
		},
		{
			name:          "set with nil value",
			key:           "key4",
			value:         nil,
			expiry:        0,
			expectedValue: nil,
			expectedErr:   errors.ErrNilValue,
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
	cache, err := hypercache.NewInMemoryWithDefaults(10)
	assert.Nil(t, err)
	defer cache.Stop()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			err = cache.Set(test.key, test.value, test.expiry)
			assert.Equal(t, test.expectedErr, err)
			if err == nil {
				val, ok := cache.Get(test.key)
				assert.True(t, ok)
				assert.Equal(t, test.expectedValue, val)
			}
		})
	}
}
