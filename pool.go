package hypercache

import (
	"sync"
)

// JobFunc is a function that can be enqueued in a worker pool.
type JobFunc func() error

// WorkerPool is a pool of workers that can execute jobs concurrently.
type WorkerPool struct {
	workers   int
	jobs      chan JobFunc
	wg        sync.WaitGroup
	quit      chan struct{}
	errorChan chan error
}

// NewWorkerPool creates a new worker pool with the given number of workers.
func NewWorkerPool(workers int) *WorkerPool {
	pool := &WorkerPool{
		workers:   workers,
		jobs:      make(chan JobFunc, workers),
		quit:      make(chan struct{}),
		errorChan: make(chan error, workers),
	}
	pool.start()
	return pool
}

// start starts the worker pool.
func (pool *WorkerPool) start() {
	for i := 0; i < pool.workers; i++ {
		go func() {
			for {
				select {
				case job := <-pool.jobs:
					err := job()
					if err != nil {
						pool.errorChan <- err
					}
					pool.wg.Done()
				case <-pool.quit:
					return
				}
			}
		}()
	}
}

// Enqueue adds a job to the worker pool.
func (pool *WorkerPool) Enqueue(job JobFunc) {
	pool.wg.Add(1)
	pool.jobs <- job
}

// Shutdown shuts down the worker pool. It waits for all jobs to finish.
func (pool *WorkerPool) Shutdown() {
	close(pool.quit)
	pool.wg.Wait()
	close(pool.jobs)
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
		for i := 0; i < diff; i++ {
			go func() {
				for job := range pool.jobs {
					job()
				}
			}()
		}
	} else {
		// Decrease the number of workers
		for i := 0; i < -diff; i++ {
			pool.quit <- struct{}{}
		}
	}
}
