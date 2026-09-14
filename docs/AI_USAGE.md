# AI assistance record

Keep this contemporaneous. Do not claim manual review or successful checks that did not happen.

## 2026-09-11: requirement analysis and environment bootstrap

AI assisted with reading the three-page project PDF, inspecting the CSV, auditing the Mac, researching installation documentation, drafting the design, and preparing the directory layout and setup scripts.

User request summary: analyze the supplied project requirements in detail; explain a Mac environment, Go application design, custom queue tradeoffs, Docker and Helm packaging, API gateway and optional UI; create a project structure under `/Users/dhirajsriharsha/Preparation/ProjectMsgQueue/`; prepare today's setup and a day-wise plan starting tomorrow.

The exact initial user prompt is retained in this conversation. Before submission, export or transcribe it into `docs/prompts/`, together with subsequent prompts. This summary is not a substitute for the required exact prompt record.

Record for every development session:
- Exact user prompt and relevant follow-up corrections.
- AI-proposed changes and files affected.
- Checks actually run and their results.
- Human review, corrections, and decisions actually performed.
- Where AI suggestions were incomplete or incorrect and the resulting fix.

Historical bootstrap validation limitations: no application existed yet; bootstrap scripts can be syntax-checked, but tool installation and Kubernetes readiness must be recorded separately after successful execution.


## 2026-09-12: Day 1 implementation

Exact instructions are preserved in `docs/prompts/2026-09-12-day1-request.txt` and the verbatim attached reference in `docs/prompts/2026-09-12-day1-reference.txt`.

Before editing, AI inspected Git status, tracked files, the reference prompt, execution plan, environment evidence and scaffold. Git was on main with no tracked modifications; several scaffold directories and Problemstatement/ were already untracked. Those inputs/placeholders were preserved. No staging, commits, branch changes, database writes, or Kubernetes deployment were performed for Day 1.

AI implemented the Go module, typed event contract, CSV parser/replayer, retrying publisher workflow, single-attempt HTTP client, bounded in-memory queue, test consumer, flag configuration, JSON slog logging, signal-aware cleanup, tests, Makefile commands, protocol/ADR documentation, and README. No external Go modules were added. The human supplied the scope/reference and had previously installed the toolchain; independent human code review is still pending.

Corrections and limitations encountered during AI development:
- Project writes and local HTTP listening initially failed under sandbox restrictions. Requested the corresponding folder and network permissions, then reran the actual checks; did not treat blocked tests as passes.
- Replaced an initial full-map dedup expiry scan with a FIFO expiry list, so each publish need not scan every retained ID.
- Corrected a test design that would mutate a running HTTP server's handler; the final handler uses an atomic request counter instead.
- Added a source record size guard so oversized measurements are counted/skipped before publication rather than becoming a permanent transport failure.
- Removed an unused consumer shutdown option rather than exposing a flag with no behavior.
- Removed stale bootstrap-blocker claims from the execution plan and distinguished proposed durable queue semantics from Day 1 behavior.

Verification executed: `make build vet race coverage demo` passed. The unit suite covers malformed CSV/header/quoting cases, replay and identity, finite runs, rate scheduling, retry-stable timestamps, queue capacity/dedup/FIFO/concurrency, HTTP validation and ambiguous acceptance, consumer response parsing, logging/configuration, and shutdown while idle, publishing, and unavailable.

The process demo launched the real queue, streamer and consumer with the supplied CSV, independently compared all 100 source measurements, and verified 100 unique event IDs, ordered rows, UTC processing times, accepted=100, received=100 and depth=0. The final run encountered seven queue-full retries, accepted all 100 and reported zero unconfirmed events. Retry count is timing-dependent and not a fixed test assertion. An actual SIGTERM/outage test exited in about 103 ms with a configured 100 ms shutdown window and unconfirmed_pending=1.

Unit coverage: overall 70.7%; queue 90.6%, queue client 87.0%, streamer 86.9%, telemetry 93.8%, config 91.4%, observability 100%. CLI entrypoints are exercised by separate subprocess tests, whose coverage is not merged into the unit profile. These results do not establish durability, production readiness, or performance at ten replicas.

No manual intervention in the code has been claimed: the corrections above were AI self-review and test-driven refinements. The reviewer should understand and independently assess the implementation before interview submission.


## 2026-09-12: Day 2 durable queue

Exact scope and reference are preserved in docs/prompts/2026-09-12-day2-request.txt and docs/prompts/2026-09-12-day2-reference.txt. Git status was reviewed before implementation and again on resume. Existing Day 1 modifications and untracked scaffold/source inputs were preserved. No staging, commit, image publishing or Kubernetes deployment was performed.

AI implemented the approved bbolt-backed single broker (pinned v1.5.0), transactional acceptance, persistent identity/content deduplication, leases with fencing tokens, ACK, delayed transient NACK, permanent quarantine, bounded cleanup and admission accounting. Changes include internal/queue/durable.go, durable_http.go and protocol.go; queueclient/config/testconsumer packages; queue and testconsumer commands; unit and subprocess tests; Makefile; README; protocol, ADR and execution-plan documentation. The streamer event and publish contract remain compatible.

AI self-review refinements included reserving completion metadata capacity during admission so ACK cannot fail because that quota filled; retaining quarantined records indefinitely within capacity rather than silently expiring them; bounding cleanup batches and exclusive database lock acquisition; and exercising transaction rollback after mutation via injected failure. That failure injection is not a real power-loss or physical disk failure test. Real subprocess tests separately use SIGKILL and restart against the same database.

Executed verification: make build vet test race coverage demo demo-day2 passed. The process tests verified a second broker's lock rejection, dropped publish response followed by restart and deduplication, consumer crash before ACK, broker restart with lease persistence, expiry and a fresh token, stale receipt rejection, and persisted ACK completion across another restart. The streamer/leased-consumer flow ended with accepted=21, acked=21, ready=0, leased=0 and completed=21. Day 1 regression and streamer SIGTERM/outage checks also passed.

Unit coverage was 76.4% overall; queue 81.7%, queueclient 87.2%, config 94.0%, observability 100%, streamer 86.9%, telemetry 93.8%, testconsumer 77.8%. CLI entrypoints run in subprocess tests, outside this unit coverage profile. Evidence is in work/day2-checks.txt and docs/DAY2_VERIFICATION.md.

Human authorization supplied the design scope and approved the storage dependency; independent human code review is still pending. AI refinements are not claimed as human review. Limits: one broker, surviving storage required, at-least-once delivery, bounded dedup retention, no lease renewal, logical accounting is not a physical file-size cap, and the diagnostic consumer is not a production durable sink. Collector/PostgreSQL/API/Helm remain subsequent work.


## 2026-09-12: Day 3 collector and PostgreSQL

Exact request and attached reference are in docs/prompts/2026-09-12-day3-request.txt and docs/prompts/2026-09-12-day3-reference.txt. Git status was reviewed before edits; the existing Day 1/Day 2 changes and untracked scaffold were preserved. The human authorized implementation and granted project/network access plus scoped Colima/Docker state access. Independent human review of the new code is pending.

AI implemented cmd/collector and cmd/migrate, bounded worker processing in internal/collector, pgx/v5 PostgreSQL storage, explicit checksummed SQL migrations, collector environment/flag configuration, health/readiness, a semantic-validation-preserving lease method, focused tests, a real-PostgreSQL test harness, Makefile targets, local PostgreSQL startup script and documentation. The public API/OpenAPI were deliberately retained for Day 4. No Dockerfiles, Helm deployment, data retention deletion, staging, commits or publishing occurred.

Design refinements from AI self-review: kept batch size one to preserve the existing queue API; inserted telemetry before metadata using a deferred foreign key so duplicates cannot regress GPU metadata; bounded per-delivery retries and global shutdown drain; separated health from readiness; preserved invalid semantic events with valid receipt identity for quarantine; suppressed database URL defaults in help after identifying a credential exposure; disabled minimum idle connections; and throttled empty polls. An initial integration test referenced the wrong broker stats method; compilation caught it and it was corrected to Snapshot. These are AI corrections, not claims of human code intervention.

Executed successfully: dependency download/tidy, formatting, make build vet test race coverage demo, compilation of the postgres_integration suite without running tests, shell syntax check and git diff --check. Collector unit coverage 91.5%; total 71.9%. The storage and migration real-database paths have not yet executed. Full results are in docs/DAY3_VERIFICATION.md and work/day3-checks.txt.

Real PostgreSQL verification is blocked: Colima initially encountered a filesystem denial, then after access was granted its VZ driver reported virtualization unavailable. make demo-day3 failed because the expected Docker context was not available. The user was asked to start the existing profile in Mac Terminal while code work continued. The SQL/real-collector crash tests are implemented and compile, but no passing PostgreSQL result is claimed. No database image version or performance result has been invented. Day 3 must not be described as fully verified until that remaining gate passes.


## 2026-09-12: Day 4 read API and generated OpenAPI

The exact Day 4 request is preserved in docs/prompts/2026-09-12-day4-request.txt. Git status, migrations, store, collector, config, Makefile, README and Day 3 tests were inspected before edits. Existing modifications/untracked files were preserved. The instruction to generate and commit YAML conflicts with the explicit no-stage/no-commit constraint; AI generated api/openapi.yaml and left it unstaged/uncommitted.

AI added cmd/api, cmd/openapi, internal/api (typed Huma operations, bounded temporary-file response preparation, status handling and tests), internal/config/api.go and tests, internal/storage/postgres/read.go, a read-only pool option in store.go, tests/day4/day4_test.go, generated api/openapi.yaml, Day 4 design/verification/ADR documentation and prompt record. Makefile now builds the API and has openapi, check-openapi and demo-day4 targets. README/execution-plan status was updated, and Day 3 verification/ADR records now distinguish the user's successful PostgreSQL 18.6 test output from the earlier agent environment blocker. No migration SQL changed.

Huma resolved to v2.39.1 and is pinned in go.mod/go.sum. Its dependency requirements upgraded golang.org/x/sync to v0.22.0, x/sys to v0.47.0 and x/text to v0.40.0; the existing unit/race and Day 1/Day 2 process regressions passed afterward. Official Huma documentation and installed module code were consulted for streaming callbacks and code-first schema generation.

AI design/review decisions: use a private, bounded temporary file so database failures always return 503 before a success body starts; emit complete arrays without row limits; bound active requests and disk space; normalize sub-microsecond query bounds correctly; reject ambiguous/invalid timestamps and unknown query parameters with 400; use one registration function for runtime and offline spec. Huma's auto-added 422 response was removed from the generated operation metadata because explicit parameter validation returns 400. A credential-help test initially matched the generic word “secret” in help prose, not an exposed password; the test was corrected to match a unique password string and passed. These are AI corrections, not human code review.

Actual successful checks: gofmt, go mod tidy, make build vet test race coverage openapi check-openapi demo, compilation-only postgres_integration test selection, a real API executable check against an unreachable DB (liveness 200, readiness/data 503, malformed filter 400, docs/spec 200, clean SIGTERM), a negative OpenAPI drift check that left its temporary input unchanged, and git diff --check. API unit coverage 92.8%; total 69.2%. PostgreSQL unit coverage is 18.3%, not proof that the real SQL paths ran. Command/subprocess coverage is not merged into the unit profile.

make demo-day4 was attempted and failed at Docker container startup because the agent environment cannot resolve colima-gpu-telemetry. No Day 4 real PostgreSQL scenarios ran; their implementation compiles and is pending user-host execution. The previously supplied successful Day 3 run is recorded as user evidence, not reused as a passing Day 4 result. Full results are in docs/DAY4_VERIFICATION.md and work/day4-*-checks.txt / work/day4-checks.txt.

Limitations: query preparation adds temporary disk I/O/latency; the explicit default 64MiB response budget produces 503 on exhaustion rather than truncation; clients must detect interrupted transfers; hard crashes can leave temporary files; only API reference documentation is provided, with no dashboard/UI. No existing data was deleted (only task-owned disposable response/test files were cleaned up). No Git reset/stage/commit/push, deployment, image publishing, schema change, retention, authentication, write API or pagination was performed. Independent human code review and Day 4 real-database verification remain pending.


## 2026-09-13: Day 5 Docker and Helm packaging

Exact user request and verbatim attachment are preserved in docs/prompts/2026-09-13-day5-request.txt and docs/prompts/2026-09-13-day5-reference.txt. Git status, module/Makefile, configuration parsers, migration executable, README/ADRs and deployment placeholders were inspected before edits. Existing Day 1–4 changes and user files were preserved. Application Go behavior, SQL schema, queue semantics and API contracts were not changed.

AI updated .dockerignore and added deploy/docker/Dockerfile, deploy/helm/gpu-telemetry (chart metadata/defaults/schema/helpers/resources/notes/README), scripts/day5-images.sh, day5-install.sh, day5-verify.sh, day5-scale.sh, tests/packaging/helm_test.go, Day 5 runbook/verification/ADR and prompt records. Makefile gained build/load/chart-check/render/package/install/verify/scale targets. README and execution-plan status were updated; Day 4's successful user-run PostgreSQL test output was recorded separately from agent-side evidence. go.mod/go.sum add yaml.v3 v3.0.1 for structural manifest tests and its test dependency resolution (kr/text v0.2.0).

The dedicated migrate image follows the Day 5 reference and replaces the earlier optional idea of bundling migrate with collector. AI identified the pre-install dependency conflict: a migration hook cannot reach ordinary PostgreSQL resources before Helm creates them. The chosen one-chart workflow first installs DB bootstrap mode and then upgrades to full mode; its pre-upgrade hook runs migrations before application rollout. This sequencing and the unsupported direct full-mode fresh install are explicitly documented. No application redesign was used to work around the issue.

Other AI review refinements: pin registry index digests; use non-root UID/fsGroup mounts and API disk-backed temporary storage; retain standalone PVCs and generated credentials; reuse Secrets through lookup; avoid creating an unused queue PVC during bootstrap (WaitForFirstConsumer compatibility); use direct pg_isready argv instead of brittle shell-escaped probe text; reject invalid replica/dedup/password values; keep the last hook Job for evidence; and verify advancing persisted/ACKed counts instead of accepting only historical data. These are AI corrections, not human code review. No independent human code review is claimed.

Executed successfully: registry manifest inspection, formatting/shell syntax checks, make build vet test race check-openapi, all five static Linux ARM64 cross-builds, make helm-check helm-template and make helm-package. Structural tests exercise selectors, credentials, mounts/security configuration, retained resources, hooks, defaults and invalid overrides. Full outcomes and evidence paths are in docs/DAY5_VERIFICATION.md.

Runtime verification is blocked in this agent environment: make docker-build failed at its missing Docker-context precheck; make helm-install failed at the missing Kubernetes-context precheck. Neither produced images nor changed a release/namespace. Loading, live ingestion, pod/PVC readiness, migration execution and scaling were not claimed as passed. The configured image names/release/namespace are intentions pending successful execution, not produced artifacts. No image/chart publishing, deployment mutation, Git stage/commit/push/reset, schema change, unrelated-file deletion or retention cleanup was performed. The chart archive itself was generated locally. User-host runtime validation and code review remain pending.

Final Day 5 compatibility review caught the removed Helm 4 `helm list --all` option. The installer now uses explicit supported status filters; packaging tests simulate fresh/existing release command flows to check bootstrap-before-upgrade ordering without touching Kubernetes. These simulated tests passed and are not deployment evidence.


## 2026-09-14: Day 7 documentation and evidence audit

User request summary: recheck recollected TC03/TC06 artifacts; begin the Day 7 documentation audit; replace the chronological README with a clear explanation of the repository, environment, bring-up scripts, builds, generated images, Helm deployment and verification; retain the old material in workStructure. The author will perform the final pushed-code clone/build/deploy test and record the demo video. The exact request remains in the conversation; private prompt files were not published.

AI reviewed Git status, Makefile, environment/image/install/verification/scale scripts, Dockerfile, Helm values, prior documentation, the private PDF requirements and external local test artifacts. Recollected TC03 had nine ordered events and one inclusive-boundary event; TC06 showed ready=0 -> 579 -> 0 and a successful verifier with 5,242 unique DB rows. These are author-run runtime results reviewed by AI, not newly executed Kubernetes tests.

Changed files: README.md, docs/workStructure.md, docs/DAY6_VERIFICATION.md, docs/SUBMISSION_AUDIT.md, docs/EXECUTION_PLAN.md and this record. AI authored the reader-oriented runbook, repository map/history, requirements mapping, evidence interpretation/fingerprints, acceptance/video checklist and current-status pointer. No application, script, chart, image or schema behavior changed. Existing untracked terminal-artifact files were preserved. No staging, commits, pushes or deployment were performed.

Commands actually executed included Git status/diff checks, source reads with cat/rg, PDF text extraction using pypdf, Python JSON ordering/boundary assertions and artifact SHA-256 fingerprints, relative-link/Make-target checks, `make helm-check`, `make helm-template` and `make check-openapi`. These validation commands passed from the project root. An initial Make/Git invocation accidentally used the task workspace and failed; it was corrected and rerun. No new full build/unit/race/coverage or clean-clone runtime success is claimed for this documentation task.

Remaining limitations: TC05 deferred; fresh-clone acceptance and video pending; exact AI prompt records remain excluded from the public repository, so that submission requirement needs a private appendix or separately reviewed record. The README warns about finite completion/dedup capacity, independent test runs and single-broker availability limits. Human review of this documentation is pending.


## 2026-09-14: Public workflow naming cleanup

The author requested removal of day-based names from public-facing commands, scripts, namespaces and README prose, while retaining day-based documentation history and carefully preserving dependencies. AI inspected Git status and traced Makefile, script, test and runbook references. Renamed the four packaging scripts to images.sh, helm-install.sh, helm-verify.sh and helm-scale.sh; renamed generated work artifacts; introduced neutral integration-test Make targets with historical aliases; changed the default namespace for new installs to gpu-telemetry. Existing namespace usage is documented explicitly in NAMING_MIGRATION.md and is covered by installer orchestration tests. No live namespace, PVC, image, application semantics or deployment changed.

The standalone PostgreSQL helper and integration tests accept neutral DOCKER_CONTEXT/POSTGRES_IMAGE variables with historical fallbacks. Existing standalone container ownership labels are accepted; new labels use postgres-dev, with the same container/volume names. README and operational runbooks use current names; historical evidence and test package directories remain traceable. Direct script callers must adopt renamed paths.

Validation passed: make helm-check (including new default/legacy namespace cases), make helm-template, make check-openapi, bash -n for all scripts, Git whitespace checks, local reference checks and dry-run Make dispatch checks. PostgreSQL-tagged test packages compiled with go test -tags=postgres_integration -run '^$' ./tests/day3 ./tests/day4; no live PostgreSQL tests were executed by that compile-only check. Live deployment validation remains the author's follow-up. No commit or push performed.

## 2026-09-14: Suspected ACK accounting bug investigation

The author requested verification of an alleged duplicate Completed increment in Durable.Ack, a minimal fix if present, retained-record/expiry/capacity regression tests, and full unit/race/vet checks. At inspected commit 171917b, Ack contained exactly one Completed increment; removeCompleted decrements once. Existing completion/recovery and bounded-cleanup tests already assert correct counts. No production fix was warranted.

Added TestCompletionAccountingMatchesRetainedRecords in internal/queue/durable_test.go: compares the counter with actual record and completion-index entries through four sequential publish/lease/ACK cycles, duplicate ACKs, exact dedup-capacity rejection, reopening, one-record cleanup batches down to zero, and a second full capacity cycle. No delivery semantics, TTL, Helm settings or persisted data changed.

Passed after the test addition: go test -count=1 ./internal/queue; go test -count=1 ./...; go test -race -count=1 ./...; go vet ./...; gofmt on the edited test; git diff --check. Initial file editing was blocked by the workspace boundary; requested project write access. Initial uncached full/race runs were blocked by local TCP listener permissions in HTTP tests; after requesting network permission both full suites passed uncached. Cached baseline results were not used as evidence for the new test. No live-cluster/image validation, commit or push was performed.
