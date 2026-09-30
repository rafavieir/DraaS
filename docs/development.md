# Development

Backend: Go 1.26, standard HTTP router, pgx, nats.go, minio-go S3 client, Zstandard and controller-runtime. Dependencies are pinned by go.mod/go.sum. UI: native ES modules, CSS tokens and embedded assets; no Node runtime is deployed. Node/Playwright are development-only browser tests.

Commands: `go test ./...`, `go vet ./...`, `go test ./internal/backup -bench . -benchmem`, `go test ./internal/backup -fuzz FuzzManifestValidation -fuzztime 10s`, `docker build --target test .`, `npm run check`, `npm run test:ui`.

On Windows the race detector requires a C compiler, and application-control policy can block temporary Go test executables. Use the supplied Linux container test target in those cases; do not disable host security policy.

All critical jobs are persisted, not queued in application memory. Add new provider implementations behind `pkg/contracts`, advertise unsupported capabilities honestly and add contract tests. Never put source credentials in manifests, events or logs.

Backend changes require image rebuild/load and a deployment restart in kind. The embedded UI is packaged into the API binary. Tests use their own synthetic workloads; they must not remove real infrastructure.
