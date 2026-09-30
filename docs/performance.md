# Performance methodology

Unit benchmarks use 4 MiB incompressible random input and report bytes/sec, allocations and latency for the actual ingest/restore pipeline. The unit memory store isolates CPU/memory behavior; those numbers are **not S3 throughput**. Simulator E2E uses highly compressible repeated synthetic data and is not a storage sizing benchmark.

```sh
go test ./internal/backup -run '^$' -bench . -benchmem
go test ./internal/backup -run '^$' -bench BenchmarkRestore -cpuprofile restore.cpu -memprofile restore.mem
go tool pprof restore.cpu
go run ./cmd/draas-bench -mib 64
```

`draas-bench` creates and removes only its own scratch file, syncs writes and hashes reads. Reads can be OS-cache dominated. It does not touch raw disks and does not yet test random IO, network, S3 or ZSvirt provisioning. Optional pprof heap/allocs/goroutine endpoints under `/api/v1/debug/pprof/` require both an admin token and `ENABLE_PPROF=true`; they are disabled by default. CPU profiles use local benchmark tooling.

Bounded execution: one message per worker process, one chunk per ingest stream, single-thread Zstandard, bounded HTTP requests and object reads, 256 MiB simulator source limit and 3-minute job deadline. Adding replicas increases concurrency; no adaptive controller exists yet. Manifests currently have a 32 MiB cap and 200,000 chunk guard; multi-TB chunk-map segmentation/resume is future work.

Required before capacity promises: incompressible S3 read/write, restore concurrency, real images at 10/100/500 GB and 1 TB, PostgreSQL EXPLAIN/load, network, storage latency, VM provisioning/boot and application readiness, under both normal load and failures. Persist baselines by environment and compare only like-for-like measurements.
