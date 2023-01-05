package hypercache

// import (
// 	"errors"
// 	"fmt"
// 	"sync"
// 	"sync/atomic"
// )

// // ClockCacheItem represents an item in the cache
// type ClockCacheItem struct {
// 	key   string          // The key used to identify the item
// 	Value interface{}     // The value stored in the item
// 	ref   int             // The reference bit
// 	prev  *ClockCacheItem // The previous item in the cache
// 	next  *ClockCacheItem // The next item in the cache
// }

// // ClockCacheItemPool is a pool of ClockCacheItem values.
// var ClockCacheItemPool = sync.Pool{
// 	New: func() interface{} {
// 		return &ClockCacheItem{}
// 	},
// }

// // Clock represents a clock cache
// type ClockCache struct {
// 	capacity  int
// 	items     map[string]*ClockCacheItem
// 	head      *ClockCacheItem
// 	mutex     sync.RWMutex
// 	cond      *sync.Cond
// 	itemCount int32
// }

// // NewClockCache creates a new clock cache with the given capacity
// func NewClockCache(capacity int) (*ClockCache, error) {
// 	if capacity < 0 {
// 		return nil, ErrInvalidCapacity
// 	}
// 	c := &ClockCache{
// 		capacity: capacity,
// 		items:    make(map[string]*ClockCacheItem, capacity),
// 	}
// 	c.cond = sync.NewCond(&c.mutex)
// 	return c, nil
// }

// // Get retrieves the value for the given key from the cache. If the key is not
// // found, it returns false.
// func (clock *ClockCache) Get(key string) (interface{}, bool) {
// 	clock.mutex.RLock()
// 	defer clock.mutex.RUnlock()
// 	item, ok := clock.items[key]
// 	if !ok {
// 		return nil, false
// 	}
// 	item.ref = 1
// 	return item.Value, true
// }

// // Set adds the key-value pair to the cache. If the cache is at capacity, it
// // evicts the least recently used item.
// func (clock *ClockCache) Set(key string, value interface{}) {
// 	clock.mutex.Lock()
// 	defer clock.mutex.Unlock()
// 	item, ok := clock.items[key]
// 	if ok {
// 		item.Value = value
// 		item.ref = 1
// 		return
// 	}
// 	for clock.itemCount == int32(clock.capacity) {
// 		go func() {
// 			// Wait for a certain amount of time before sending a signal on the condition variable.
// 			// time.Sleep(1 * time.Second)
// 			clock.cond.Signal()
// 		}()
// 		clock.cond.Wait()
// 	}
// 	item = ClockCacheItemPool.Get().(*ClockCacheItem)
// 	item.key = key
// 	item.Value = value
// 	item.ref = 0
// 	clock.items[key] = item
// 	if clock.head == nil {
// 		clock.head = item
// 		clock.head.prev = item
// 		clock.head.next = item
// 	} else {
// 		item.prev = clock.head.prev
// 		item.next = clock.head
// 		clock.head.prev.next = item
// 		clock.head.prev = item
// 		clock.head = item
// 	}
// 	clock.itemCount++

// 	clock.cond.Broadcast()
// }

// // Evict removes the least recently used item from the cache.
// func (clock *ClockCache) Evict() (string, error) {
// 	clock.mutex.Lock()
// 	defer clock.mutex.Unlock()
// 	fmt.Println("eviction called")
// 	for {
// 		fmt.Println("evicting")
// 		if clock.head == nil {
// 			clock.cond.Broadcast()
// 			return "", errors.New("cache is empty")
// 		}
// 		item := clock.head
// 		if item.ref == 0 {
// 			key := item.key
// 			if item.prev == item {
// 				clock.head = nil
// 			} else {
// 				clock.head = item.next
// 				item.prev.next = item.next
// 				item.next.prev = item.prev
// 			}
// 			delete(clock.items, key)
// 			ClockCacheItemPool.Put(item)
// 			atomic.AddInt32(&clock.itemCount, -1)
// 			clock.cond.Broadcast()
// 			return key, nil
// 		}
// 		item.ref = 0
// 		clock.head = item.next
// 	}
// }

// // func (clock *ClockCache) Evict() (keys []string, err error) {
// // 	clock.mutex.Lock()
// // 	defer clock.mutex.Unlock()

// // 	for {
// // 		if clock.head == nil {
// // 			clock.cond.Broadcast()
// // 			return nil, errors.New("cache is empty")
// // 		}

// // 		item := clock.head
// // 		if item.ref == 0 {
// // 			keys = append(keys, item.key)
// // 			if item.prev == item {
// // 				clock.head = nil
// // 			} else {
// // 				clock.head = item.next
// // 				item.prev.next = item.next
// // 				item.next.prev = item.prev
// // 			}
// // 			delete(clock.items, item.key)
// // 			ClockCacheItemPool.Put(item)
// // 		} else {
// // 			item.ref = 0
// // 			clock.head = item.next
// // 		}

// // 		if len(clock.items) < clock.capacity {
// // 			clock.cond.Broadcast()
// // 			break
// // 		}
// // 	}

// // 	return keys, nil
// // }

// // Delete removes the key-value pair with the given key from the cache.
// func (clock *ClockCache) Delete(key string) {
// 	clock.mutex.Lock()
// 	defer clock.mutex.Unlock()
// 	item, ok := clock.items[key]
// 	if !ok {
// 		return
// 	}
// 	delete(clock.items, key)
// 	if item.prev == item {
// 		clock.head = nil
// 	} else {
// 		item.prev.next = item.next
// 		item.next.prev = item.prev
// 		if clock.head == item {
// 			clock.head = item.next
// 		}
// 	}
// 	ClockCacheItemPool.Put(item)
// 	atomic.AddInt32(&clock.itemCount, -1)
// 	clock.cond.Broadcast()
// }
