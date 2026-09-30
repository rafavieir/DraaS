# ADR 001 — Go modular services and Kubernetes
Accepted. Use Go for all backend components. Separate API, worker and operator by lifecycle and privileges; share domain packages. Kubernetes is the control plane, not the primary backup medium. Local kind is a lab only. Host infrastructure bootstrap and production HA require supplied hardware/topology.
