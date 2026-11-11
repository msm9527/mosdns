# Patched Mihomo delivery

These scripts build the native `direct-system` outbound and
`tun.system-route-mark` support used by MSM home-access listeners. They preserve
ordinary Mihomo interface bindings. Smart also receives an ANYTLS stream
isolation repair so closing one stream cannot close its shared TLS session.
Smart also repairs optional model/ASN dependencies, data
collection, and maintenance across configuration reloads. These changes retain
the upstream selection and weight algorithms.

Meta/Alpha revision `msm.2` and Smart revision `msm.3` synchronize manual
selection with status, TCP/UDP proxy selection, and background quality checks.
Selector, URLTest, Fallback, and Smart groups take atomic selection snapshots;
discarding a stale selection cannot erase a newer user choice. URLTest also
serializes recalculations that overlap a selection reset, protecting its cached
fast node while retaining its existing cached fast path and tolerance rules.

Smart manual node changes apply to new connections while healthy existing streams
finish on their original exits. Automatic winner adoption updates future pins
without closing in-flight responses; confirmed failure/degradation still closes
affected connections. Only the first unpinned dial for a target is coordinated,
independently for TCP and UDP. Followers read its committed winner and open their
own connections; cached pins and manual selections do not wait on this gate.
Caller/group cancellation wakes waiters, failed first dials release their entry,
and retries remain bounded. Manual dial failures preserve automatic pins;
automatic fallback failures still clear stale pins for recovery.

Smart's default formula-based selection does not require a model or CGO.
LightGBM inference uses the upstream pure-Go leaves evaluator. When enabled,
`/proxies/<group>` reports `lightGBMReady` and `lightGBMError` in addition to the
configured `useLightGBM` switch. A failed initial load falls back to the formula;
a successful model update activates inference in the same running groups.
Downloads reject HTTP errors, invalid/empty/truncated models, models over 64 MiB,
and models requiring more than 4096 feature slots. Candidates are validated
before replacing the installed model; a failed reload keeps working inference.
Automatic model updates apply enable/disable and interval changes together on
configuration reload. One scheduler handles all revisions, cancels its old
automatic request when policy changes, and keeps manual updates independent.
Enabling updates checks for a missing or stale model immediately. Repeated
registration does not create extra workers or reset the timer. Enabled intervals
must be positive hour counts that fit a Go duration; invalid values are rejected
before applying configuration.

`preferASN` likewise remains the configured switch. `asnReady` and `asnError`
report effective availability. Missing or invalid ASN data disables only that
enhancement, preserving traffic. A valid database installed later is detected
within five seconds without a core restart. Existing ready readers retain the
upstream geodata update behavior.

Collectors keep a stable writer through temporary configuration parsing and
reload, flush every buffered row when groups close, and reopen safely when
other groups continue. Shared maintenance runs while any Smart group remains;
replacing the first group no longer cancels maintenance for its replacements.
Adaptive connection deadlines retain at least one second for transient jitter
and proxy handshakes, bounded by a shorter explicit caller limit. A previous
one-millisecond LAN handshake must not reduce future attempts to five milliseconds.

The marked system-route bypass is a Linux feature. MSM must generate its
`direct-system` helpers only on Linux; enabling a nonzero TUN
`system-route-mark` on another OS is rejected. The other release targets retain
their ordinary proxy functionality and interface binding behavior.

The Linux TUN bypass records the exact routing rules it owns in a durable
journal. After an interrupted start or abrupt process exit, the next start
recovers only verified rules from that journal. Unrelated rules and active
instances are protected by namespace, boot, process, and rule identity checks.

## Release contracts

- Meta and Alpha retain the existing `mihomo` release, asset names, archive
  formats, and 15-target platform/CPU matrix. Their binary and release-body
  versions append `-msm.2` to the official upstream version.
- Smart uses the independent `mihomo-smart` release. Assets preserve the official
  `mihomo-<os>-<arch>-alpha-smart-<12-character-upstream-SHA>.gz` format. AMD64
  variants explicitly use `amd64-v1` and `amd64-v3`; the baseline always builds
  with `GOAMD64=v1`.
- Smart covers Linux AMD64 v1/v3, 386, ARM v5/v6/v7, ARM64, and Darwin AMD64
  v1/v3 and ARM64. Its version is `alpha-smart-<12-character-SHA>-msm.3`.
- Smart sources follow the official `Prerelease-Alpha` release tag, not the
  moving Alpha branch. Release metadata and assets must agree with the resolved
  source commit. All channel checkouts use an immutable full upstream SHA.
- Each release includes SHA256SUMS and source/provenance JSON containing the
  full upstream SHA, applied patch SHA-256 hashes, features, and passed gates.
  GitHub's individual asset digest remains the download verification source.
- MSM must obtain native Smart from this repository's `mihomo-smart` release.
  Falling back to an unpatched official core would remove the required native
  capability. The official-style filename alone does not advertise the patch;
  use the Smart version line in the release body and the binary version.

## Local verification

Use a disposable upstream Git checkout. The verifier changes it in place:

```sh
python3 scripts/mihomo/verify-source.py meta /path/to/mihomo-source --sudo-tests
python3 scripts/mihomo/verify-source.py alpha /path/to/mihomo-source --sudo-tests
python3 scripts/mihomo/verify-source.py smart /path/to/mihomo-source --sudo-tests
```

Go, Python 3, Git, and Linux iproute2 are required. The integration tests use
Linux network namespaces and real sockets, so they require CAP_NET_ADMIN,
CAP_SYS_ADMIN, and CAP_NET_RAW. `--sudo-tests` compiles as the current user and
runs only test executables with sudo. Omit it inside an appropriately privileged
test container. Required tests must actually run and pass; skips fail the gate.
Every channel also runs native group-selection tests with Go's race detector,
including concurrent selection/status/data-path calls and deterministic stale
selection cleanup. Smart adds its own group and data-path regressions. These
host tests require a working C compiler; release binaries remain built with
`CGO_ENABLED=0`. The proof records the exact required `race_tests`, which the
packaging script validates together with the source and patch fingerprints.
The gate also compiles and executes the actual non-Linux validation stub with
explicit Go source files, so unsupported marked TUN configurations must fail
preflight while ordinary and disabled TUN configurations remain accepted.

`--unit-only` and `--apply-only` help local development but cannot authorize
packaging. The build script requires a successful full verification record for
the exact channel, upstream SHA, and patch fingerprints. An unknown upstream
patch context fails the build; it must be reviewed and rebased before release.
Re-running the verifier on its own already-patched checkout is supported.

For a verified checkout, build with the environment used in the workflows:

```sh
CHANNEL=smart CHANNEL_VERSION='alpha-smart-<12-character-SHA>-msm.3' \
  ASSET_SUFFIX=linux-amd64-v1 PACKAGE_FORMAT=gz OUTPUT_DIR=/path/to/artifacts \
  GOOS=linux GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0 \
  bash /path/to/mosdns/scripts/mihomo/build-artifact.sh
```

Changing a shared routing patch requires reviewing all three upstream channels.
Each changed channel advances its `msm.N` revision and regression requirements.
The shared selection repair is revision `msm.2` for Meta/Alpha; Smart combines
it with its lifecycle and Smart-group repairs in revision `msm.3`.
The workflows run on schedule or explicit dispatch;
editing or pushing these files does not publish a release.
