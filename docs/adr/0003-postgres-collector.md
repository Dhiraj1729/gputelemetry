# ADR 0003: PostgreSQL collector and commit-before-ACK

Status: implemented and verified, including the user-provided successful PostgreSQL integration run (see DAY3_VERIFICATION.md).

Use a shared PostgreSQL database and pgx/v5 with a small per-process pool. Each bounded worker leases one message, matching the existing Day 2 API. The collector validates, persists in a database transaction, commits and only then ACKs. Database errors and uncertain commits leave the message unacknowledged. There is no application-wide insertion mutex.

Telemetry's event_id primary key and ON CONFLICT DO NOTHING provide idempotent writes under redelivery and concurrent collectors. Insert telemetry before GPU metadata, using a deferred foreign key, so an already-stored event changes neither ingestion time nor metadata. GPU observation bounds and metadata updates handle out-of-order processing deterministically. Preserve raw labels, workload fields and a JSON event copy alongside queryable columns.

Migrations are explicit, embedded, checksummed and transactional. A migration-only advisory lock coordinates runners; collectors never run migrations. No automatic retention, down migration or deletion API is introduced.

Separate liveness from database readiness. Stop acquisition on signals and drain under a bounded deadline. Keep the public read API and generated OpenAPI together on Day 4 to avoid establishing a temporary API contract without its required documentation and tests.

Limits: no lease renewal, no broker/database distributed transaction, no exactly-once claim, no replicas/performance verification in Day 3. A future bulk lease/insert protocol can be introduced after measuring the one-message baseline. Supporting a batch of one now avoids extending queue semantics as a side effect of adding persistence.
