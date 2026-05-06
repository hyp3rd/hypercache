---
title: RFCs
---

# RFCs

Design proposals — accepted, rejected, or implemented — that informed
the architecture. Every RFC is dated and tracked through to its
final disposition.

| # | Title | Status |
|---|---|---|
| [0001](0001-backend-owned-eviction.md) | Backend-owned eviction | **Closed — Rejected** (spike measured, hypothesis falsified, code removed) |
| [0002](0002-generic-item-typing.md) | Generic `Item[V]` typing | **Phase 1 implemented** (the `Typed[T, V]` wrapper); Phase 2 (deep generics) deferred to v3 |

## When to write one

For changes whose blast radius extends beyond a single PR — wire formats,
public API shape, multi-phase refactors, or anything that needs a paper
trail of "we tried X and it didn't work, here's why" so future
contributors don't re-tread the same ground.

Skip the RFC for bug fixes, internal refactors, and feature work whose
shape is already obvious from the code.
