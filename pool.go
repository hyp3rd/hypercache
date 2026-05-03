package hypercache

import (
	"sync"
)

// JobFunc is a function that can be enqueued in a worker pool.
type JobFunc func() error

// WorkerPool is a pool of workers that can execute jobs concurrently.
//
// Enqueue is safe to call concurrently with Shutdown. After Shutdown returns,
// further Enqueue calls silently drop the job — this prevents races during
// graceful cache shutdown where background loops (expiration, eviction) may
// still attempt to enqueue work after Stop() has begun.
type WorkerPool struct {
	// shutdownMu protects the closed flag and serializes Shutdown vs Enqueue
	// so Shutdown cannot close pool.jobs while an Enqueue is mid-send.
	// Enqueue takes RLock (concurrent senders allowed); Shutdown takes Lock.
	shutdownMu sync.RWMutex
	closed     bool

	workers   int
	jobs      chan JobFunc
	wg        sync.WaitGroup
	quit      chan struct{}
	errorChan chan error
}

// NewWorkerPool creates a new worker pool with the given number of workers.
func NewWorkerPool(workers int) *WorkerPool {
	pool := &WorkerPool{
		workers: workers,
		jobs:    make(chan JobFunc, workers),
		// buffer quit to allow multiple resize signals without blocking immediately
		quit:      make(chan struct{}, workers),
		errorChan: make(chan error, workers),
	}
	pool.start()

	return pool
}

// Enqueue adds a job to the worker pool. If the pool has been shut down,
// the job is silently dropped (see WorkerPool docstring).
func (pool *WorkerPool) Enqueue(job JobFunc) {
	pool.shutdownMu.RLock()
	defer pool.shutdownMu.RUnlock()

	if pool.closed {
		return
	}

	pool.wg.Add(1)

	pool.jobs <- job
}

// Shutdown shuts down the worker pool. It waits for all enqueued jobs to
// finish before returning. Idempotent — repeat calls are no-ops.
func (pool *WorkerPool) Shutdown() {
	pool.shutdownMu.Lock()

	if pool.closed {
		pool.shutdownMu.Unlock()

		return
	}

	pool.closed = true
	// Close jobs while holding the write lock so no Enqueue can race the close.
	close(pool.jobs)
	pool.shutdownMu.Unlock()

	// Wait for all enqueued jobs to complete
	pool.wg.Wait()
	// Now signal any lingering workers to exit select loop
	close(pool.quit)
	// It's now safe to close the error channel (no more sends after wg completes)
	close(pool.errorChan)
}

// Errors returns a channel that can be used to receive errors from the worker pool.
func (pool *WorkerPool) Errors() <-chan error {
	return pool.errorChan
}

// Resize resizes the worker pool.
func (pool *WorkerPool) Resize(newSize int) {
	if newSize < 0 {
		return
	}

	diff := newSize - pool.workers
	if diff == 0 {
		return
	}

	pool.workers = newSize

	if diff > 0 {
		// Increase the number of workers
		for range diff {
			go pool.worker()
		}
	} else {
		// Decrease the number of workers
		// Send only the number of quit signals needed to remove workers
		for range -diff {
			pool.quit <- struct{}{}
		}
	}
}

// start starts the worker pool.
func (pool *WorkerPool) start() {
	for range pool.workers {
		go pool.worker()
	}
}

// worker is the main loop executed by each worker goroutine.
func (pool *WorkerPool) worker() {
	for {
		select {
		case job, ok := <-pool.jobs:
			if !ok {
				// jobs channel closed and drained
				return
			}

			err := job()
			if err != nil {
				pool.errorChan <- err
			}

			pool.wg.Done()

		case <-pool.quit:
			return
		}
	}
}
