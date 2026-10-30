# Patched Mihomo delivery

These scripts build the native `direct-system` outbound and
`tun.system-route-mark` support used by MSM home-access listeners. They preserve
ordinary Mihomo interface bindings. Smart also receives an ANYTLS stream
isolation repair so closing one stream cannot close its shared TLS session.

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
  versions append `-msm.1` to the official upstream version.
- Smart uses the independent `mihomo-smart` release. Assets preserve the official
  `mihomo-<os>-<arch>-alpha-smart-<12-character-upstream-SHA>.gz` format. AMD64
  variants explicitly use `amd64-v1` and `amd64-v3`; the baseline always builds
  with `GOAMD64=v1`.
- Smart covers Linux AMD64 v1/v3, 386, ARM v5/v6/v7, ARM64, and Darwin AMD64
  v1/v3 and ARM64. Its version is `alpha-smart-<12-character-SHA>-msm.1`.
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
CHANNEL=smart CHANNEL_VERSION='alpha-smart-<12-character-SHA>-msm.1' \
  ASSET_SUFFIX=linux-amd64-v1 PACKAGE_FORMAT=gz OUTPUT_DIR=/path/to/artifacts \
  GOOS=linux GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0 \
  bash /path/to/mosdns/scripts/mihomo/build-artifact.sh
```

Changing a patch requires reviewing all three upstream channels, updating the
regression requirements, and advancing the `msm.N` revision before publishing a
new patch generation. The workflows run on schedule or explicit dispatch;
editing or pushing these files does not publish a release.
