# ADR 011 - Scheduled Recovery Validation

Accepted for P3D design. Recovery testing becomes a product capability through scheduled policies, not a manual lab-only action.

A recovery test policy selects recovery points, creates an isolated sandbox, restores, boots, validates guest/application state, measures RTO, signs a report and cleans up. Scheduling must include jitter and support latest verified, random within retention and specific recovery point strategies.

Validation freshness is stateful. Workloads can be RECENT, STALE or NEVER_TESTED according to policy thresholds. Test failure keeps evidence; cleanup failure is represented separately from validation failure.