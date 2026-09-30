# ADR 002 — PostgreSQL + NATS JetStream + external S3
Accepted. PostgreSQL stores catalog, jobs and transactional outbox. JetStream delivers durable at-least-once messages with explicit acknowledgement. Bounded retries and SQL job locks handle duplication. S3 stores payloads and self-contained manifests. Object Lock is a separate capability and must not be claimed by ordinary object storage.
