# ADR 0004: typed read API with complete response preparation

Status: implemented and verified, including the user-provided successful PostgreSQL integration output (see DAY4_VERIFICATION.md).

Use Huma with net/http, typed requests and typed response element schemas. Runtime and offline OpenAPI generation share one registration function. The generated YAML is a build artifact, and check-openapi checks byte-for-byte freshness. No database connection is necessary to generate the spec. The explicit no-commit constraint takes precedence over the reference's instruction to commit generated YAML.

Read persisted PostgreSQL rows through visitor methods in an API pool configured for read-only transactions. Preserve the existing schema, apply parameterized inclusive time bounds and deterministic ordering, and do not add pagination or a row-count cap. Normalize sub-microsecond bounds to preserve PostgreSQL comparison precision.

Direct network streaming cannot guarantee a 503 for database failures after a 200 header has been sent. Instead, encode rows one at a time into a private temporary file, then transfer the completed array with Content-Length. This keeps RAM bounded and maps all database failures before response transfer to 503. The tradeoff is delayed first byte and temporary disk I/O; concurrency, preparation deadlines and an explicit byte budget bound resources. Exceeding a budget is a failed request, never partial success. Transfer interruptions still require client-side completeness checks.

The API communicates only with PostgreSQL. It owns no ingestion, ACKs, migrations or deletion workflow. Health/readiness, environment configuration, bounded pool/request concurrency and signal-aware shutdown prepare it for later packaging. Dockerfiles, Helm, authentication, retention and application UI are outside this milestone.
