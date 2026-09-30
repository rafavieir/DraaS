# ADR 005 — Replace unavailable MinIO container distribution in the lab

2026-09-28. Pulls from the former public MinIO Docker Hub and Quay tags failed. Official source is archived, so a new installation must not silently depend on a previously cached image. Use the official RustFS S3-compatible container for the isolated local laboratory and verify conditional writes in integration tests. The Go S3 adapter remains compatible with MinIO, AWS and other conforming stores. This does not select a production storage platform or claim WORM/HA qualification.

References: https://github.com/minio/minio ; https://docs.rustfs.com/en/installation/container/docker . Tested image pinned in bootstrap: `sha256:8cc9801755448b71a786705ce76692c77e14936cccd87cf2fc31842e58f4d1ff`. Live contract tests verify conditional creation and corruption rejection.
