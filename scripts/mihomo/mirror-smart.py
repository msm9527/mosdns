#!/usr/bin/env python3
"""Prepare byte-for-byte upstream Smart artifacts; never compile or patch them."""

import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import urllib.request


UPSTREAM_REPOSITORY = "vernesong/mihomo"
UPSTREAM_TAG = "Prerelease-Alpha"
RELEASE_URL = f"https://github.com/{UPSTREAM_REPOSITORY}/releases/tag/{UPSTREAM_TAG}"
ASSET_URL = f"https://github.com/{UPSTREAM_REPOSITORY}/releases/download/{UPSTREAM_TAG}/"
TARGETS = (
    "linux-amd64-v1", "linux-amd64-v3", "linux-386",
    "linux-armv5", "linux-armv6", "linux-armv7", "linux-arm64",
    "darwin-amd64-v1", "darwin-amd64-v3", "darwin-arm64",
)
ASSET_PATTERN = re.compile(
    r"^mihomo-(" + "|".join(TARGETS) + r")-alpha-smart-([0-9a-f]{7,40})\.gz$"
)
MAX_ARCHIVE_SIZE = 256 << 20


def fetch_release():
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "MSM-Smart-Mirror"}
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(
        f"https://api.github.com/repos/{UPSTREAM_REPOSITORY}/releases/tags/{UPSTREAM_TAG}",
        headers=headers,
    )
    with urllib.request.urlopen(request, timeout=60) as response:
        return json.load(response)


def select_assets(release):
    if release.get("tag_name") != UPSTREAM_TAG or release.get("html_url") != RELEASE_URL or release.get("draft"):
        raise ValueError("unexpected upstream Smart release")
    selected = {}
    for asset in release.get("assets", []):
        matched = ASSET_PATTERN.fullmatch(asset.get("name", ""))
        if matched is None:
            continue
        target, revision = matched.groups()
        if target in selected:
            raise ValueError(f"ambiguous upstream Smart assets for {target}")
        if asset.get("browser_download_url") != ASSET_URL + asset["name"]:
            raise ValueError(f"untrusted upstream asset URL: {asset['name']}")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", asset.get("digest") or ""):
            raise ValueError(f"missing trusted SHA-256: {asset['name']}")
        if type(asset.get("size")) is not int or not 0 < asset["size"] <= MAX_ARCHIVE_SIZE:
            raise ValueError(f"invalid archive size: {asset['name']}")
        updated = datetime.fromisoformat(asset.get("updated_at", "").replace("Z", "+00:00"))
        if updated.tzinfo is None:
            raise ValueError("upstream asset timestamp requires a timezone")
        selected[target] = {key: asset[key] for key in (
            "name", "browser_download_url", "digest", "size", "updated_at"
        )} | {"revision": revision}
    missing = set(TARGETS) - selected.keys()
    if missing:
        raise ValueError(f"missing upstream platforms: {sorted(missing)}")
    if len({asset["revision"] for asset in selected.values()}) != 1:
        raise ValueError("upstream Smart assets contain mixed revisions")
    return [selected[target] for target in TARGETS]


def download_asset(asset, directory):
    destination = directory / asset["name"]
    temporary = destination.with_suffix(".gz.part")
    digest, size = hashlib.sha256(), 0
    # Artifacts are public. Never forward the API token to an asset/CDN host.
    request = urllib.request.Request(asset["browser_download_url"], headers={"User-Agent": "MSM-Smart-Mirror"})
    try:
        with urllib.request.urlopen(request, timeout=90) as response, temporary.open("xb") as output:
            while chunk := response.read(1 << 20):
                size += len(chunk)
                if size > asset["size"]:
                    raise ValueError(f"archive exceeds upstream size: {asset['name']}")
                digest.update(chunk)
                output.write(chunk)
        if size != asset["size"] or "sha256:" + digest.hexdigest() != asset["digest"]:
            raise ValueError(f"upstream SHA-256/size mismatch: {asset['name']}")
        temporary.replace(destination)
    finally:
        temporary.unlink(missing_ok=True)
    return asset


def prepare(directory):
    directory.mkdir(parents=True, exist_ok=True)
    if any(directory.iterdir()):
        raise ValueError("output directory must be empty; refusing to mix old artifacts")
    assets = select_assets(fetch_release())
    with ThreadPoolExecutor(max_workers=4) as pool:
        list(pool.map(lambda asset: download_asset(asset, directory), assets))
    # The rolling release may change during downloads. Do not publish a mixed
    # snapshot or silently label old artifacts as the current upstream release.
    if select_assets(fetch_release()) != assets:
        raise ValueError("upstream release changed during download; retry with a fresh directory")
    manifest = {
        "channel": "smart", "delivery": "upstream-original",
        "upstream_repository": UPSTREAM_REPOSITORY, "upstream_tag": UPSTREAM_TAG,
        "upstream_release_url": RELEASE_URL,
        "version": "alpha-smart-" + assets[0]["revision"],
        "patched": False, "recompiled": False, "verified": True,
        "assets": assets,
    }
    (directory / "mihomo-smart-source.json").write_text(json.dumps(manifest, indent=2) + "\n")
    checksums = []
    for path in sorted(directory.iterdir()):
        checksums.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n")
    (directory / "SHA256SUMS").write_text("".join(checksums))
    return manifest


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    result = prepare(args.output_dir)
    print(f"Verified {len(result['assets'])} original upstream artifacts: {result['version']}")
