package hypercache

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWorkerPool_EnqueueAndShutdown(t *testing.T) {
	pool := NewWorkerPool(3)

	var mu sync.Mutex

	results := []int{}

	// Enqueue 5 jobs
	for i := range 5 {
		val := i
		pool.Enqueue(func() error {
			mu.Lock()

			results = append(results, val)

			mu.Unlock()

			return nil
		})
	}

	pool.Shutdown()

	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}
}

func TestWorkerPool_JobErrorHandling(t *testing.T) {
	pool := NewWorkerPool(2)
	expectedErr := errors.New("job error")
	pool.Enqueue(func() error {
		return expectedErr
	})
	pool.Enqueue(func() error {
		return nil
	})

	go func() {
		time.Sleep(100 * time.Millisecond)
		pool.Shutdown()
	}()

	var gotErr error
	for err := range pool.Errors() {
		if errors.Is(err, expectedErr) {
			gotErr = err
		}
	}

	if gotErr == nil {
		t.Errorf("expected error to be received from Errors channel")
	}
}

func TestWorkerPool_ResizeIncrease(t *testing.T) {
	pool := NewWorkerPool(1)

	var mu sync.Mutex

	count := 0

	for range 10 {
		pool.Enqueue(func() error {
			time.Sleep(10 * time.Millisecond)
			mu.Lock()

			count++

			mu.Unlock()

			return nil
		})
	}

	pool.Resize(5)
	pool.Shutdown()

	if count != 10 {
		t.Errorf("expected 10 jobs to be processed, got %d", count)
	}
}

func TestWorkerPool_ResizeDecrease(t *testing.T) {
	pool := NewWorkerPool(4)

	var mu sync.Mutex

	count := 0

	for range 8 {
		pool.Enqueue(func() error {
			time.Sleep(10 * time.Millisecond)
			mu.Lock()

			count++

			mu.Unlock()

			return nil
		})
	}

	pool.Resize(2)
	pool.Shutdown()

	if count != 8 {
		t.Errorf("expected 8 jobs to be processed, got %d", count)
	}
}

func TestWorkerPool_ResizeToZeroAndBack(t *testing.T) {
	pool := NewWorkerPool(2)
	done := make(chan struct{})
	called := false

	pool.Resize(0)
	pool.Enqueue(func() error {
		called = true

		close(done)

		return nil
	})

	// Resize back to 1 so the job can be processed
	pool.Resize(1)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("job was not processed after resizing back up")
	}

	pool.Shutdown()

	if !called {
		t.Errorf("expected job to be called after resizing back up")
	}
}

func TestWorkerPool_NegativeResizeDoesNothing(t *testing.T) {
	pool := NewWorkerPool(2)
	pool.Resize(-1)

	if pool.workers != 2 {
		t.Errorf("expected workers to remain 2, got %d", pool.workers)
	}

	pool.Shutdown()
}

func TestWorkerPool_EnqueueAfterShutdownPanics(t *testing.T) {
	pool := NewWorkerPool(1)
	pool.Shutdown()

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic when enqueuing after shutdown")
		}
	}()

	pool.Enqueue(func() error { return nil })
}
