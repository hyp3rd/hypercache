# PRD

Overview
Hypercache is a thread‑safe caching library with pluggable backends and eviction algorithms, already targeting Go 1.25 and generics. The design exposes a service interface that supports middleware layers for decorating cache operations.

Findings & Suggestions
Ticker Lifecycle – Background jobs create time.Tickers but never stop them, which can leak resources; explicitly call tick.Stop() when terminating the loops.

WorkerPool Resize Logic – Resizing the pool sends a quit signal pool.workers times regardless of how many workers need removal; adjust to send -diff signals and consider buffering to avoid blocking.

Expired‑Key Semantics – GetMultiple returns ErrKeyExpired, yet tests expect ErrKeyNotFound for expired items, indicating a behavior mismatch or test expectation issue.

Per‑Call Goroutines – Get and GetWithInfo spawn a goroutine for each expired item, which may create overhead under heavy load; consider synchronous cleanup or a dedicated worker to batch expirations.

Object Pool Hygiene – ItemPoolManager returns items to the pool without resetting fields, risking retention of large values; zero the struct before reusing it.

Modern Go Features – The project already uses Go 1.25; further modernization could include:

Generics for typed values rather than any, providing compile‑time safety.

Use of maps.Clone/maps.DeleteFunc and slices.SortFunc for cleaner collection handling.

errors.Is/errors.Join to simplify error comparisons.

Potential Enhancements

Expose a MGet/MSet on backends for efficient multi-key operations.

Provide context-aware cancellation in WorkerPool and background jobs to ease shutdown.

Add methods such as Contains, Peek, Increment, or TTL refresh APIs.

Integrate OpenTelemetry metrics and tracing for observability.
