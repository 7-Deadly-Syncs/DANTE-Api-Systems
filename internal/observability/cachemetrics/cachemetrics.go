package cachemetrics

import (
	"sort"
	"sync"
	"sync/atomic"
)

const (
	MerchantCacheHits            = "merchant_cache_hits_total"
	MerchantCacheMisses          = "merchant_cache_misses_total"
	MerchantNegativeCacheHits    = "merchant_negative_cache_hits_total"
	MerchantLockAcquired         = "merchant_lock_acquired_total"
	MerchantLockContention       = "merchant_lock_contention_total"
	TransactionStatusCacheHits   = "transaction_status_cache_hits_total"
	TransactionStatusCacheMisses = "transaction_status_cache_misses_total"
)

// SnapshotEntry is a JSON-friendly metrics point.
type SnapshotEntry struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"`
}

// Recorder records cache metric counters using Prometheus-friendly names.
type Recorder interface {
	Inc(name string)
	Snapshot() []SnapshotEntry
}

// InMemoryRecorder stores counters in-process and can later be wrapped by Prometheus collectors.
type InMemoryRecorder struct {
	mu       sync.RWMutex
	counters map[string]*atomic.Uint64
}

// NewInMemoryRecorder constructs an in-memory recorder with known cache counters pre-registered.
func NewInMemoryRecorder() *InMemoryRecorder {
	r := &InMemoryRecorder{
		counters: map[string]*atomic.Uint64{},
	}

	for _, name := range []string{
		MerchantCacheHits,
		MerchantCacheMisses,
		MerchantNegativeCacheHits,
		MerchantLockAcquired,
		MerchantLockContention,
		TransactionStatusCacheHits,
		TransactionStatusCacheMisses,
	} {
		r.counters[name] = &atomic.Uint64{}
	}

	return r
}

// Inc increments the named counter.
func (r *InMemoryRecorder) Inc(name string) {
	counter := r.counter(name)
	counter.Add(1)
}

// Snapshot returns all known counters in stable name order.
func (r *InMemoryRecorder) Snapshot() []SnapshotEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.counters))
	for name := range r.counters {
		names = append(names, name)
	}
	sort.Strings(names)

	snapshot := make([]SnapshotEntry, 0, len(names))
	for _, name := range names {
		snapshot = append(snapshot, SnapshotEntry{
			Name:  name,
			Value: r.counters[name].Load(),
		})
	}

	return snapshot
}

func (r *InMemoryRecorder) counter(name string) *atomic.Uint64 {
	r.mu.RLock()
	counter, ok := r.counters[name]
	r.mu.RUnlock()
	if ok {
		return counter
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if counter, ok = r.counters[name]; ok {
		return counter
	}

	counter = &atomic.Uint64{}
	r.counters[name] = counter
	return counter
}package cachemetrics

import (
	"sort"
	"sync"
	"sync/atomic"
)

const (
	MerchantCacheHits            = "merchant_cache_hits_total"
	MerchantCacheMisses          = "merchant_cache_misses_total"
	MerchantNegativeCacheHits    = "merchant_negative_cache_hits_total"
	MerchantLockAcquired         = "merchant_lock_acquired_total"
	MerchantLockContention       = "merchant_lock_contention_total"
	TransactionStatusCacheHits   = "transaction_status_cache_hits_total"
	TransactionStatusCacheMisses = "transaction_status_cache_misses_total"
)

// SnapshotEntry is a JSON-friendly metrics point.
type SnapshotEntry struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"`
}

// Recorder records cache metric counters using Prometheus-friendly names.
type Recorder interface {
	Inc(name string)
	Snapshot() []SnapshotEntry
}

// InMemoryRecorder stores counters in-process and can later be wrapped by Prometheus collectors.
type InMemoryRecorder struct {
	mu       sync.RWMutex
	counters map[string]*atomic.Uint64
}

// NewInMemoryRecorder constructs an in-memory recorder with known cache counters pre-registered.
func NewInMemoryRecorder() *InMemoryRecorder {
	r := &InMemoryRecorder{
		counters: map[string]*atomic.Uint64{},
	}

	for _, name := range []string{
		MerchantCacheHits,
		MerchantCacheMisses,
		MerchantNegativeCacheHits,
		MerchantLockAcquired,
		MerchantLockContention,
		TransactionStatusCacheHits,
		TransactionStatusCacheMisses,
	} {
		r.counters[name] = &atomic.Uint64{}
	}

	return r
}

// Inc increments the named counter.
func (r *InMemoryRecorder) Inc(name string) {
	counter := r.counter(name)
	counter.Add(1)
}

// Snapshot returns all known counters in stable name order.
func (r *InMemoryRecorder) Snapshot() []SnapshotEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.counters))
	for name := range r.counters {
		names = append(names, name)
	}
	sort.Strings(names)

	snapshot := make([]SnapshotEntry, 0, len(names))
	for _, name := range names {
		snapshot = append(snapshot, SnapshotEntry{
			Name:  name,
			Value: r.counters[name].Load(),
		})
	}

	return snapshot
}

func (r *InMemoryRecorder) counter(name string) *atomic.Uint64 {
	r.mu.RLock()
	counter, ok := r.counters[name]
	r.mu.RUnlock()
	if ok {
		return counter
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if counter, ok = r.counters[name]; ok {
		return counter
	}

	counter = &atomic.Uint64{}
	r.counters[name] = counter
	return counter
}
