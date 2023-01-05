package hypercache

import (
	"errors"
	"time"
)

type ClockAlgorithm struct {
	items map[string]*CacheItem
}

func NewClockAlgorithm(capacity int) (*ClockAlgorithm, error) {
	if capacity < 0 {
		return nil, ErrInvalidCapacity
	}

	return &ClockAlgorithm{
		items: make(map[string]*CacheItem, capacity),
	}, nil
}

func (c *ClockAlgorithm) Evict() (string, error) {
	var oldestKey string
	oldestTime := time.Now()
	for key, item := range c.items {
		if item.LastAccess.Before(oldestTime) {
			oldestTime = item.LastAccess
			oldestKey = key
		}
	}
	if oldestKey == "" {
		return "", errors.New("clock is empty")
	}
	c.Delete(oldestKey)
	return oldestKey, nil
}

func (c *ClockAlgorithm) Set(key string, value interface{}) {
	item := CacheItemPool.Get().(*CacheItem)
	item.Value = value
	item.LastAccess = time.Now()
	item.AccessCount = 0
	c.items[key] = item
}

func (c *ClockAlgorithm) Get(key string) (interface{}, bool) {
	item, ok := c.items[key]
	if !ok {
		return nil, false
	}
	item.LastAccess = time.Now()
	item.AccessCount++
	return item.Value, true
}

func (c *ClockAlgorithm) Delete(key string) {
	item, ok := c.items[key]
	if !ok {
		return
	}
	delete(c.items, key)
	CacheItemPool.Put(item)
}
