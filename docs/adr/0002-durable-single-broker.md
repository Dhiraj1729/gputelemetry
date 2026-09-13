# ADR 0002: durable single-broker delivery

Status: implemented, 12 September 2026.

The user explicitly approved bbolt in the Day 2 reference. Pin bbolt v1.5.0 and use atomic updates with normal sync enabled. Queue semantics remain our code. Use a separate ReliableStore interface and durable HTTP handler so historical destructive receive remains isolated in explicit memory mode. Durable is now the executable default.

Persist events and fingerprints, state, due-time/sequence indexes, attempts, tokens, last NACK outcome, completion records and counters. Cryptographic tokens fence expired/superseded deliveries. Pending identities never expire; completions expire from ACK time. A duplicate ACK is recognized by the successful token within completion retention. An identical NACK can be retried only while its resulting ready/quarantine state remains current; a newer lease supersedes that token.

Reserve completion identity capacity at publish, rather than discovering insufficient metadata room after successful processing. Include leased and quarantined messages in total count/byte admission limits. Quarantine has indefinite retention plus hard capacity; automatic cleanup must not silently discard unacknowledged events. Operator quarantine maintenance is deferred.

Use small indexed cleanup batches rather than database scans. Polling and timers run outside transactions. A context-aware gate bounds wait for database access and permits shutdown cancellation, while bbolt remains the atomicity/durability authority. Actual fsync cannot be safely canceled by a context; forced process termination recovers committed state if storage survives.

Keep one active broker and exclusive bounded file lock. This provides restart recovery, not high availability or replicated durability. A future Helm chart needs one StatefulSet replica and storage retained on uninstall. Automatic live compaction, lease renewal and broker replication would expand scope and are deferred.

Verified using real SIGKILL/restart tests, lost response injection, stale receipts, transaction rollback injection, concurrent consumers, and the unchanged streamer. Do not claim exactly-once side effects: the production collector must commit PostgreSQL first and deduplicate by event ID before ACK.
