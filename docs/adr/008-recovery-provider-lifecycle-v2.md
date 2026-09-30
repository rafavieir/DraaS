# ADR 008 - Recovery Provider Lifecycle v2

Accepted for P3D. The provider contract is split into idempotent primitives instead of hiding an entire recovery workflow inside one blocking call. DraaS owns the workflow state machine, retries, admission and persistence. Providers execute primitives and report durable handles.

Every mutable provider operation receives a stable operation ID. Retries must reuse provider-side resources when the first request succeeded but the response was lost. Provider handles include resource ID, optional task ID, cluster and metadata, and must be persisted in the catalog as workflow state evolves.

The v2 capability model exposes console, hot attach, network lifecycle, guest agent, reboot, force stop, tagging and async task support. Future ZSvirt support must fit this contract without another structural redesign.

Legacy simulator validation remains supported while providers migrate. `production_ready` remains false until state-machine resume, idempotency and failure tests pass for real providers.