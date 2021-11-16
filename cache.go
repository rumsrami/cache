package cache

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

type Item struct {
	Object     interface{}
	Expiration int64
}

// Returns true if the item has expired.
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	return time.Now().UnixNano() > item.Expiration
}

const (
	// For use with functions that take an expiration time.
	NoExpiration time.Duration = -1
	// For use with functions that take an expiration time. Equivalent to
	// passing in the same expiration duration as was given to New() or
	// NewFrom() when the cache was created (e.g. 5 minutes.)
	DefaultExpiration time.Duration = 0
)

type Cache struct {
	*cache
	// If this is confusing, see the comment at the bottom of New()
}

type cache struct {
	sync.RWMutex
	defaultExpiration time.Duration
	items             map[interface{}]Item
	onEvicted         func(interface{}, interface{})
	janitor           *janitor
}

// Add an item to the cache, replacing any existing item. If the duration is 0
// (DefaultExpiration), the cache's default expiration time is used. If it is -1
// (NoExpiration), the item never expires.
func (c *cache) Set(k string, x interface{}, d time.Duration) {
	// "Inlining" of set
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = time.Now().Add(d).UnixNano()
	}
	c.Lock()
	defer c.Unlock()
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
	// TODO: Calls to mu.Unlock are currently not deferred because defer
	// adds ~200 ns (as of go1.)
}

func (c *cache) set(k interface{}, x interface{}, d time.Duration) {
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = time.Now().Add(d).UnixNano()
	}
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
}

// Add an item to the cache only if an item doesn't already exist for the given
// key, or if the existing item has expired. Returns an error otherwise.
func (c *cache) Add(k interface{}, x interface{}, d time.Duration) error {
	c.Lock()
	defer c.Unlock()

	_, found := c.get(k)
	if found {
		return fmt.Errorf("Item %s already exists", k)
	}
	c.set(k, x, d)
	return nil
}

// Set a new value for the cache key only if it already exists, and the existing
// item hasn't expired. Returns an error otherwise.
func (c *cache) Replace(k interface{}, x interface{}, d time.Duration) error {
	c.Lock()
	defer c.Unlock()

	_, found := c.get(k)
	if !found {
		return fmt.Errorf("Item %s doesn't exist", k)
	}
	c.set(k, x, d)
	return nil
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(k string) (interface{}, bool) {
	c.RLock()
	defer c.RUnlock()

	// "Inlining" of get and Expired
	item, found := c.items[k]
	if !found {
		return nil, false
	}
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}
	return item.Object, true
}

// GetAndExtend an item from the cache. Returns the item or
// nil, and a bool indicating  whether the key was found. The item's
// expiration time is extended by d, if found.
func (c *cache) GetAndExtend(k interface{}, d time.Duration) (interface{}, bool) {
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}

	c.Lock()
	defer c.Unlock()

	item, found := c.get(k)
	if !found {
		return nil, false
	}

	if d > 0 {
		c.set(k, item.Object, d)
	}
	return item.Object, true
}

type loader func(k interface{}) (interface{}, time.Duration, error)

// GetOrLoad an item from the cache. If the key is present in the cache,
// return it's item. Otherwise load a new item using the load() callback, add
// it to the cache and return it.
func (c *cache) GetOrLoad(k interface{}, load loader) (interface{}, error) {
	c.Lock()
	defer c.Unlock()

	item, found := c.get(k)
	if !found {
		object, d, err := load(k)
		if err == nil {
			c.set(k, object, d)
		}
		return object, err
	}

	return item.Object, nil
}

// GetAndExtendOrLoad an item from the cache. If the key is present in the cache,
// return it's item and extend it's expiration. Otherwise load a new item using
// the load() callback, add it to the cache and return it.
func (c *cache) GetAndExtendOrLoad(k interface{}, d time.Duration, load loader) (interface{}, error) {
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}

	c.Lock()
	defer c.Unlock()

	item, found := c.get(k)
	if !found {
		object, d, err := load(k)
		if err == nil {
			c.set(k, object, d)
		}
		return object, err
	}

	if d > 0 {
		c.set(k, item.Object, d)
	}
	return item.Object, nil
}

func (c *cache) get(k interface{}) (*Item, bool) {
	item, found := c.items[k]
	if !found {
		return nil, false
	}
	// "Inlining" of Expired
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}
	return &item, true
}

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(k interface{}) {
	c.Lock()
	v, evicted := c.delete(k)
	c.Unlock()
	if evicted {
		c.onEvicted(k, v)
	}
}

func (c *cache) delete(k interface{}) (interface{}, bool) {
	if c.onEvicted != nil {
		if v, found := c.items[k]; found {
			delete(c.items, k)
			return v.Object, true
		}
	}
	delete(c.items, k)
	return nil, false
}

type keyAndValue struct {
	key   interface{}
	value interface{}
}

// Delete all expired items from the cache.
func (c *cache) DeleteExpired() {
	var evictedItems []keyAndValue
	now := time.Now().UnixNano()
	c.Lock()
	for k, v := range c.items {
		// "Inlining" of expired
		if v.Expiration > 0 && now > v.Expiration {
			ov, evicted := c.delete(k)
			if evicted {
				evictedItems = append(evictedItems, keyAndValue{k, ov})
			}
		}
	}
	c.Unlock()
	for _, v := range evictedItems {
		c.onEvicted(v.key, v.value)
	}
}

// Sets an (optional) function that is called with the key and value when an
// item is evicted from the cache. (Including when it is deleted manually, but
// not when it is overwritten.) Set to nil to disable.
func (c *cache) OnEvicted(f func(interface{}, interface{})) {
	c.Lock()
	defer c.Unlock()

	c.onEvicted = f
}

// Returns the number of items in the cache. This may include items that have
// expired, but have not yet been cleaned up.
func (c *cache) ItemCount() int {
	c.Lock()
	defer c.Unlock()

	return len(c.items)
}

// Delete all items from the cache.
func (c *cache) Flush() {
	var evictedItems []keyAndValue
	now := time.Now().UnixNano()
	c.Lock()
	for k, v := range c.items {
		// "Inlining" of expired
		if v.Expiration <= 0 || now <= v.Expiration {
			ov, evicted := c.delete(k)
			if evicted {
				evictedItems = append(evictedItems, keyAndValue{k, ov})
			}
		}
	}
	c.items = map[interface{}]Item{}
	c.Unlock()
	for _, v := range evictedItems {
		c.onEvicted(v.key, v.value)
	}
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *cache) {
	j.stop = make(chan bool)
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func stopJanitor(c *Cache) {
	c.janitor.stop <- true
}

func runJanitor(c *cache, ci time.Duration) {
	j := &janitor{
		Interval: ci,
	}
	c.janitor = j
	go j.Run(c)
}

func newCache(de time.Duration, m map[interface{}]Item) *cache {
	if de == 0 {
		de = -1
	}
	c := &cache{
		defaultExpiration: de,
		items:             m,
	}
	return c
}

func newCacheWithJanitor(de time.Duration, ci time.Duration, m map[interface{}]Item) *Cache {
	c := newCache(de, m)
	// This trick ensures that the janitor goroutine (which--granted it
	// was enabled--is running DeleteExpired on c forever) does not keep
	// the returned C object from being garbage collected. When it is
	// garbage collected, the finalizer stops the janitor goroutine, after
	// which c can be collected.
	C := &Cache{c}
	if ci > 0 {
		runJanitor(c, ci)
		runtime.SetFinalizer(C, stopJanitor)
	}
	return C
}

// Return a new cache with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one (or NoExpiration),
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
func New(defaultExpiration, cleanupInterval time.Duration) *Cache {
	items := make(map[interface{}]Item)
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, items)
}

// Return a new cache with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one (or NoExpiration),
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
//
// NewFrom() also accepts an items map which will serve as the underlying map
// for the cache. This is useful for starting from a deserialized cache
// (serialized using e.g. gob.Encode() on c.Items()), or passing in e.g.
// make(map[string]Item, 500) to improve startup performance when the cache
// is expected to reach a certain minimum size.
//
// Only the cache's methods synchronize access to this map, so it is not
// recommended to keep any references to the map around after creating a cache.
// If need be, the map can be accessed at a later point using c.Items() (subject
// to the same caveat.)
//
// Note regarding serialization: When using e.g. gob, make sure to
// gob.Register() the individual types stored in the cache before encoding a
// map retrieved with c.Items(), and to register those same types before
// decoding a blob containing an items map.
func NewFrom(defaultExpiration, cleanupInterval time.Duration, items map[interface{}]Item) *Cache {
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, items)
}
