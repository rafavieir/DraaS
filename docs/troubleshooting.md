# Troubleshooting

| Symptom | Diagnosis and action |
|---|---|
| Docker pipe absent | Start Docker Desktop, wait for Linux engine, run `docker info`. |
| Public MinIO pull denied | Lab uses pinned official RustFS; no dependence on removed MinIO distribution tags. |
| API CrashLoopBackOff | Read API logs; check PostgreSQL/NATS readiness and external S3 container. Startup dependency timeout is bounded. |
| S3 connection refused | Check `docker ps --filter name=draas-lab-s3`, external port 19000 and `host.docker.internal` reachability from the kind node. |
| Pod pending | Check node resources, PVC binding and namespace events. |
| Stored but not verified | Inspect verify jobs, worker logs and NATS; STORED is not used for healthy RPO. |
| Job RETRYING | Inspect error/correlation; three processing attempts are permitted. Resolve the dependency and submit a new action after FAILED. |
| UI 401/403 | Use generated token; viewer cannot mutate. Browser refresh requires re-entry because tokens are memory-only. |
| UI not updated after rebuild | Load new image into kind and restart project deployments. |
| Go test blocked by Windows policy | Run Linux `docker build --target test`; do not disable application control. |
| No production RTO shown | Expected. A real guest boot/application test and capacity qualification have not been performed. |

Do not delete keys, the backup volume or namespace to fix a transient failure. Preserve evidence and inspect logs first.
