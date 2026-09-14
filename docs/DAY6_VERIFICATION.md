# Day 6 evidence review

Reviewed 14 September 2026 from author-supplied `Day6-TestCases.txt` and the separate local artifacts directory. This is a review of saved command output, not a claim that the documentation editor reran the live cluster tests. Raw files remain outside this repository.

**Status: core functional tests passed; TC05 maximum-scale runtime exercise deferred.** The final clean-clone acceptance run and demonstration video are still pending.

| Case | Result | Evidence and limits |
| --- | --- | --- |
| TC01 | Pass | `TC01-terminal.txt`: build, unit/race tests, coverage 69.2%, OpenAPI current. Several Go results were cached. |
| TC02 | Pass | `TC02-terminal-ext.txt`: verifier PASS, 1,706 rows equal 1,706 distinct IDs, healthy deployment/migration/PVCs. |
| TC03 | Pass, rerun | New terminal log dated 14 September: 247 GPUs, nine telemetry records, ordering true, one exact inclusive-boundary result. Saved JSON independently checked during this audit. |
| TC04 | Pass | `TC04-scaled-state.txt`, `TC04-helm-verify-3x3.txt`, `TC04-final-state.txt`: 1/1 to 3/3 to 1/1 and verifier PASS. |
| TC05 | Deferred | 10 streamers and 10 collectors not run due to laptop resource constraints. No maximum-scale performance claim. |
| TC06 | Pass, rerun | Ready count 0 -> 579 -> 0; ACK count 3,498 -> 3,617 -> 4,983. Recovered accepted=ACKed=4,983, leased=quarantined=0, redeliveries=1. New verifier PASS with 5,242 distinct DB rows. |
| TC07 | Pass within test run | Queue pod UID changed; bound queue PVC retained volume `pvc-c9ccd9fc-84e2-49b4-bcbe-cc552b9584d1`; verifier PASS. Pre-restart backlog was empty: not a dedicated pending-backlog crash test. |
| TC08 | Pass | Collector replaced and Ready; DB total/distinct counts advanced 5,762 -> 6,427; verifier PASS. Pod deletion alone does not establish a forced crash specifically between commit and ACK. |
| TC09 | Pass for original final snapshot | Final desired/Ready streamer and collector counts 1/1, queue=1, both PVCs Bound, verifier PASS, 8,256 total/distinct rows. This snapshot predates the TC03/TC06 rerun. |

## Corrections and interpretation

The first TC03 ordering expression failed in jq; the first TC06 verifier exited at its queue port-forward process check. The recollected files now show successful executions. Do not present the initial attempts as passes. The earlier failures are recorded here from the preceding review; their original raw files were replaced at the same paths in the supplied directory.

Different test groups have different PVC identities and database counters. Treat them as separate test runs; do not combine counts into a single continuous durability claim. Generic `day5-*.json` filenames can be overwritten by each verifier run; prefer case-specific captures and timestamps.

Queue counters demonstrate backlog drainage and a redelivery in the TC06 rerun; they do not alone prove an exhaustive event-by-event no-loss invariant. Equal DB total/distinct counts demonstrate unique persisted event identities, not exactly-once delivery. Stronger commit-before-ACK crash evidence lives in the Day 3 PostgreSQL test suite and its separately recorded results.

No throughput benchmark, sustained 10/10 test, hardware resource profile, or PostgreSQL outage exercise in Kubernetes is established by this artifact set. Earlier real-PostgreSQL integration tests cover outage/timeout behavior separately.

## Rerun artifact fingerprints

SHA-256 fingerprints identify the exact files reviewed, without copying the author's raw telemetry/logs into the public repository.

| File | SHA-256 |
| --- | --- |
| `TC03-terminal.txt` | `84d862a3d74248ad0be8eae3c089285ddf0363b1f4acfa73f67bebcdeb593543` |
| `TC03-gpus.json` | `2ddacabfb719265a790803e47622299297f8a2523c395457f8e819e9c9cc1a65` |
| `TC03-telemetry-full.json` | `fab09ef44c714fab954817981ce3c2bc3795ea7c94426d9073722d31562dcbcc` |
| `TC03-telemetry-bounded.json` | `4b9c850d63e689dc3a58c2072d28a88537da0816211f38ddabe74c211698f83b` |
| `TC06-initial-queue-stats.json` | `c6f8030f8730dff4c4443e1ddb0331ffdaef5db3a0a2da21fe9d6bdd68c162e0` |
| `TC06-paused-queue-stats.json` | `e6592196eb2efa60dc52a5bdd4e8af85b053cb0a19a36a73d144b1f0403b8613` |
| `TC06-recovered-queue-stats.json` | `7ab83071efc4accae1b16a1f2e23abdaa7930f5f69783ec9b91c253b7275ff39` |
| `TC06-helm-verify.txt` | `a28cd51c67af1832529064489ce582dd647a724efa3273d5ab7e42e434520594` |
