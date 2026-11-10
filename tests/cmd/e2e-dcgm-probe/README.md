# Direct DCGM probe

`cache-counts --hostengine HOST:PORT --field-id ID --output /tmp/cache.json`
reads existing watched samples without installing another watch. Its version 3
private report preserves sample counts, integer history (including native
status and timestamps), and GPU identities queried directly from DCGM. The
standalone restart scenario compares its exporter energy samples against this
history from the replacement hostengine. Energy queries include parent GPUs
alongside MIG instances; retention queries keep their existing instance
selection. Build the probe and suite from the same revision and pass `-dcgm-probe-binary` for direct native suite runs.

`field-entity-history` checks DCGM cache wiring with a test-created GPU, GPU
instance, and one compute instance. It watches XID field 230, injects timestamped
integer samples with the entity-aware test ABI, and compares independently read
native history with those samples. A second read proves the requested cutoff.
Missing native prerequisites fail the command; this path never passes by skip.

Build and run embedded mode on a host with compatible driver/DCGM libraries:

```bash
go build -o /tmp/e2e-dcgm-probe ./tests/cmd/e2e-dcgm-probe
sudo /tmp/e2e-dcgm-probe field-entity-history \
  --mode embedded --output /tmp/native-entity-history.json
```

Embedded mode owns and closes its DCGM session. The report is private test
input and is written only after owned watches and groups are cleaned up.

For standalone mode, the caller must first start an isolated hostengine with a
unique Unix socket in its own scratch directory. The caller also owns stopping
that process and removing its scratch files on success and failure:

```bash
sudo /tmp/e2e-dcgm-probe field-entity-history --mode standalone \
  --owned-hostengine unix:///tmp/my-owned-dcgm/engine.sock \
  --output /tmp/native-entity-history.json
```

`--owned-hostengine` explicitly asserts that the backend is isolated and owned
by this test. There is no shared `localhost:5555` fallback. The probe releases
its watches, groups, and connection; fake entities and their injected caches
remain until the caller stops that backend. Do not point this mutation command
at a shared DCGM service.

The probe uses the paired DCGM 4.7 runtime's `dcgmInjectEntityFieldValue`
symbol. The pinned go-dcgm test header instead names
`dcgmEntityInjectFieldValue`; its GPU-only Go injection wrapper cannot inject a
CI. Native ABI code stays in this test command; the JSON contract under
`tests/internal/dcgmprobe` stays cgo-free for host tests.

The report describes fake topology and includes typed native entity IDs,
observed NVML instance IDs, timestamps, statuses, values, and both read windows.
Native fake CIs can share NVML compute-instance IDs, so this probe deliberately
creates one CI. Deterministic multi-CI fixtures must separately prove distinct
NVML IDs 0 and 1. This command proves native injection/history feasibility; it
does not prove exporter COUNT/TOTAL rendering, physical MIG discovery, or a
real driver-generated XID. Final exporter acceptance must run against its exact
candidate and record each native mode separately.

## Exporter acceptance with the core candidate

`field-entity-exporter` requires the field/entity collection core candidate in
the same checkout. The native probe branch alone does not fix exporter
COUNT/TOTAL collection. Build a reviewed combined candidate and record its
source heads, tree, binary checksum and build metadata before qualification.

The command starts the actual exporter with a private CSV and a loopback HTTP
address. After readiness it injects separate timestamped parent-GPU, GI, CI,
plain-GPU and unselected-control histories. It compares two HTTP scrapes against
an independent native history read. After the first matching scrape, another
distinct nonzero XID sample is injected on each selected entity and independently read into native history. The second
scrape must include those new codes with a total of one while preserving all
earlier totals, proving polling progress and rejecting replay inflation. The
version 2 report retains the initial `history` and full `secondHistory` for the
respective scrapes. COUNT includes zero observations and excludes
the older sample; TOTAL excludes zero and the pre-start sample.

```bash
go build -o /tmp/e2e-dcgm-probe ./tests/cmd/e2e-dcgm-probe
sudo /tmp/e2e-dcgm-probe field-entity-exporter --mode embedded \
  --scenario ci-derived --output /tmp/native-exporter-ci-derived.json
sudo /tmp/e2e-dcgm-probe field-entity-exporter --mode embedded \
  --scenario ci-raw --output /tmp/native-exporter-ci-raw.json
sudo /tmp/e2e-dcgm-probe field-entity-exporter --mode embedded \
  --scenario plain --output /tmp/native-exporter-plain.json
sudo /tmp/e2e-dcgm-probe field-entity-exporter --mode embedded \
  --scenario mixed --output /tmp/native-exporter-mixed.json
```

Repeat each scenario in a fresh caller-owned standalone backend, using the
isolation and caller cleanup described above:

```bash
sudo /tmp/e2e-dcgm-probe field-entity-exporter --mode standalone \
  --owned-hostengine unix:///tmp/my-owned-dcgm/engine.sock \
  --scenario ci-derived --output /tmp/native-exporter-standalone-ci.json
```

Use one probe process per scenario and a fresh backend for each standalone
run; old fake entities and cached values invalidate the fixture. `ci-derived`
enables only derived families. `ci-raw` also checks the latest raw gauge.
`plain` checks one selected plain GPU. `mixed` selects the parent GPU, its GI
and one plain GPU; their own histories must appear exactly once.

The test-owned client delegates the native API and retains real cleanup until
the independent oracle is closed and the exporter has retired its collectors.
If retirement cannot be established, cleanup is withheld and the command
fails: exit the owned probe process and stop its owned standalone backend.
Reports are written only after successful assertions and cleanup. Native
prerequisites and an injection before the first TOTAL poll are required;
failure never becomes a passing skip.

These runs use one fake CI because native fake NVML compute IDs can all be
zero. They supplement the mandatory deterministic CI-ID 0/1 fixture. They
prove cache/watch/history/exporter wiring on the tested runtime, and do not
establish real MIG discovery or driver-generated XIDs. Record each backend,
mode and scenario separately; unexecuted native modes remain unverified.

## MIG XID counters

The `ci-only` scenario checks XID COUNT/TOTAL values and labels for one MIG
compute instance using exporter code compiled into the probe. Parent GPU and
GPU-instance histories stay empty; samples from an unselected GPU must not
appear in metrics.

```bash
sudo /tmp/e2e-dcgm-probe field-entity-exporter --mode embedded \
  --scenario ci-only --output /tmp/native-exporter-ci-only.json
```

Recent samples `45, 45, 0, 46` produce COUNT values `45=2`, `0=1` and `46=1`;
TOTAL excludes zero. An older sample `13` is excluded from both families.
A second scrape must include the new sample `73` once and preserve earlier
totals. Instance labels must match the NVML IDs and GPU-instance profile.

Empty parents have zero COUNT without an `xid` label and no TOTAL samples.
Their DCGM `NO_DATA` records remain in the report. Failed attempts write
`<output>.failed.json` with the failed step, history-verification flags, last
HTTP response and cleanup status. Saved histories and responses have size
limits; shortened data is marked.
