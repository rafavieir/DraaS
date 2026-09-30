# ADR 007 — Recovery readiness and commercial activation are separate

Accepted. TEST defaults to non-billable and uses temporary resources. Recovery produces READY, never ACTIVE automatically. ACTIVATE DR requires explicit tenant-authorized confirmation and an immutable RatePlan snapshot. Only a persisted DRActivationSession can authorize metering. DEACTIVATE DR must resolve dirty data and stop/release resources according to the selected policy before closing usage by default.

Use versioned persistent events, an append-only usage ledger with source-event uniqueness and decimal/fixed-point currency. Technical test usage is distinct from billable usage. No payment processor or fiscal invoicing. Default boundaries are ON_ACTIVATION and ON_RESOURCES_RELEASED. Test prices are explicitly lab examples, not a customer contract.

Console access is separately authorized, short-lived, tenant/VM-bound and audited. ACTIVE_DR resources must never be deleted by test TTL cleanup. Failback/reprotection require explicit treatment of data; unsupported actions fail closed.
