---
description: "Use when changing releaseui packages, release process APIs, the local release server, run persistence, or release UI architecture."
name: "Release UI architecture"
applyTo: "releaseui/**/*.go"
---

# Release UI architecture

- This code is an unreleased prototype. Break APIs and move packages if that helps create the best architecture and clear organization.
- Keep the root `releaseui` package as the basic caller-facing API. It should configure and start the UI without exposing the internal HTTP application.
- Treat package boundaries as the main organization tool. A concept with its own state, invariants, validation, or tests should usually be a package.
- Keep standard startup behavior in the root package. Always report the launch URL when the listener is ready. Model actual choices, such as opening a browser, as options and keep platform-specific implementation internal.
- Avoid families of files distinguished by prefixes such as `process_registry.go`, `process_execution.go`, and `process_state.go`. Extract the concept behind the prefix into a package, then give files inside that package simple names.
- Name a package's primary implementation file after the package, such as `runstore/runstore.go` and `localserver/localserver.go`. Use a different filename only when it names a genuinely separate concept within that package.
- Keep a package's primary type together with its constructor and methods. Put supporting DTOs and option declarations before the primary type, then place the primary type immediately before its constructor and contiguous method block. Put unrelated package functions and private helpers afterward. Do not insert types or free functions between the primary type, its constructor, or its methods.
- Keep `internal/server` limited to HTTP routing, request and response translation, embedded assets, and listener lifecycle. Keep all `Server` methods together in `server.go`; do not spread endpoint categories across files.
- Focused server test files may group listener, process-route, and release-route behavior. Keep shared test doubles in `fixtures_test.go`, and put lifecycle or persistence validation tests in the package that owns that behavior.
- Keep process planning, execution, checkpointing, restoration, and durable record history in `internal/processrun`. `processrun.Manager` owns runtime sessions, while `processrun.History` owns record queries, selection, export, and import.
- Keep mutable plans, runners, records, and locks scoped to one configured process. Do not reintroduce a server-wide active release.
