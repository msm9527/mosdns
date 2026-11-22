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

## Meta/Alpha unmodified upstream builds

The `mihomo` workflow builds Meta and Alpha directly from pinned, unmodified
[MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo) commits without any MSM
patches or `-msm.*` version suffixes. It keeps the 15-target platform matrix and
archive names (`mihomo-meta-*.tar.gz` / `mihomo-alpha-*.tar.gz` / `.exe`) for
compatibility while verifying clean Git state (`vcs.modified=false`), exact
`vcs.revision`, and `go mod verify` via `scripts/mihomo/build-upstream.sh`.

For a clean upstream checkout:

```sh
CHANNEL=meta CHANNEL_VERSION='v1.19.34' EXPECTED_COMMIT='<40-char-sha>' \
  ASSET_SUFFIX=linux-amd64 PACKAGE_FORMAT=tar.gz \
  GOOS=linux GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0 \
  bash /path/to/mosdns/scripts/mihomo/build-upstream.sh /path/to/upstream /path/to/artifacts
```

Legacy patch scripts and regression fixtures remain archived for reference with
prior releases; neither the `mihomo` nor `mihomo-smart` workflows apply them.
