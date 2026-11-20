# Mihomo delivery

## Smart: original upstream artifacts

The `mihomo-smart` release mirrors the original binaries from
[vernesong/mihomo Prerelease-Alpha](https://github.com/vernesong/mihomo/releases/tag/Prerelease-Alpha).
The scheduled/manual workflow downloads existing archives. It does not check
out the Smart source, apply patches, invoke Go, or recompress the archives.
Binary versions and filenames keep the upstream revision without an `msm.N`
suffix.

The same ten platforms are published: Linux AMD64 v1/v3, 386, ARM v5/v6/v7,
ARM64, and Darwin AMD64 v1/v3 and ARM64. AMD64 v1 is selected explicitly;
upstream's unqualified AMD64 archive is not substituted for it.

`scripts/mihomo/mirror-smart.py` verifies the official HTTPS release origin,
complete platform set, one common revision, trusted SHA-256 digests, and archive
sizes. A changing or ambiguous upstream release fails preparation. Each archive
is copied byte for byte. `mihomo-smart-source.json` records the official URLs,
digests and version with `patched: false` and `recompiled: false`; `SHA256SUMS`
also covers that manifest.

Publication uploads all verified replacements before removing stale release
assets. GitHub's uploaded asset digests must match the local files before the
old patched binaries are retired and the release metadata is updated. The
`mihomo-smart` URL and release identity are retained. Its tag follows the
publishing workflow commit; the mirrored binary's revision comes from upstream
asset metadata, not this repository's source.

Local preparation (no publication):

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/mihomo -p 'test_mirror_smart.py'
python3 scripts/mihomo/mirror-smart.py --output-dir /path/to/empty/staging-directory
```

MSM installs Smart directly from the author's release. Both the direct source
and this mirror contain the same original archives. Original Smart does not
provide the private `direct-system` or `tun.system-route-mark` extensions;
configurations using those features require a compatible core or a changed
home-access deployment. Optional model/ASN preparation and configuration
integration belong to MSM.

## Meta/Alpha native builds

The separate `mihomo` workflow still builds Meta/Alpha with the native
`direct-system` outbound and `tun.system-route-mark` support used by MSM
home-access listeners. Ordinary interface bindings are preserved. The release
keeps its existing names, archive formats and 15-target platform matrix with
revision `msm.4`.

The shared patches synchronize manual selection with status and TCP/UDP
selection. They preserve health-check thresholds above one and make a threshold
of one trigger the first eligible check. Linux TUN routing supports exact port
masks and records only owned routes in a journal for crash recovery. Unknown
dependency layouts fail verification; existing unrelated rules are protected.

Use a disposable upstream checkout because the verifier changes it in place:

```sh
python3 scripts/mihomo/verify-source.py meta /path/to/mihomo-source --sudo-tests
python3 scripts/mihomo/verify-source.py alpha /path/to/mihomo-source --sudo-tests
```

Go, Python 3, Git and Linux iproute2 are required. Namespace routing tests need
CAP_NET_ADMIN, CAP_SYS_ADMIN and CAP_NET_RAW. Required tests must pass without
skips, including race tests and the non-Linux validation stub. Packaging requires
a successful verification record matching the source and patch fingerprints;
`--unit-only` and `--apply-only` cannot authorize packaging.

For a verified checkout:

```sh
CHANNEL=meta CHANNEL_VERSION='vX.Y.Z-msm.4' \
  ASSET_SUFFIX=linux-amd64 PACKAGE_FORMAT=tar.gz OUTPUT_DIR=/path/to/artifacts \
  GOOS=linux GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0 \
  bash /path/to/mosdns/scripts/mihomo/build-artifact.sh
```

Legacy Smart patches and their regression fixtures remain available for
comparison with prior releases; neither the Smart mirror nor its publishing
workflow invokes them. They do not describe capabilities of the original
upstream artifacts now distributed by `mihomo-smart`.
