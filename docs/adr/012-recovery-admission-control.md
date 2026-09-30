# ADR 012 - Recovery Admission Control

Accepted for P3D design. Heavy recovery work must pass admission before consuming provider and storage capacity. Disaster events can produce many simultaneous requests, so the platform needs bounded queues, priority and fairness.

Initial priorities are P0 active disaster/emergency, P1 critical disaster, P2 normal disaster, P3 recovery test and P4 audit/deep verify. Recovery tests may be preempted by disaster work. Active DR VMs must never be preempted automatically.

Admission tracks estimated CPU, RAM, restore storage, object-store throughput, network, provider API capacity, disk throughput and worker capacity. Queue delay is accounted separately from engine execution time when reporting RTO impact.