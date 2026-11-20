"""Integrity regressions for publishing upstream Smart without source patches."""

import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("mirror_smart", Path(__file__).with_name("mirror-smart.py"))
mirror = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mirror)


class SmartMirrorTest(unittest.TestCase):
    payload = b"official-archive-bytes"

    def release(self):
        assets = []
        for target in mirror.TARGETS:
            name = f"mihomo-{target}-alpha-smart-6a80483.gz"
            assets.append({"name": name, "browser_download_url": mirror.ASSET_URL + name,
                           "digest": "sha256:" + hashlib.sha256(self.payload).hexdigest(),
                           "size": len(self.payload), "updated_at": "2026-11-17T17:34:27Z"})
        return {"tag_name": mirror.UPSTREAM_TAG, "html_url": mirror.RELEASE_URL, "assets": assets}

    def test_selects_exact_existing_platforms(self):
        release = self.release()
        release["assets"].append({"name": "mihomo-linux-amd64-alpha-smart-6a80483.gz"})
        release["assets"].append({"name": "mihomo-linux-amd64-v1-go120-alpha-smart-6a80483.gz"})
        self.assertEqual(10, len(mirror.select_assets(release)))

    def test_rejects_untrusted_or_incomplete_metadata(self):
        cases = {
            "missing platform": lambda r: r["assets"].pop(),
            "duplicate platform": lambda r: r["assets"].append(copy.deepcopy(r["assets"][0])),
            "missing digest": lambda r: r["assets"][0].update(digest=None),
            "bad digest": lambda r: r["assets"][0].update(digest="sha256:invalid"),
            "oversized": lambda r: r["assets"][0].update(size=mirror.MAX_ARCHIVE_SIZE + 1),
            "zero size": lambda r: r["assets"][0].update(size=0),
            "wrong source": lambda r: r["assets"][0].update(browser_download_url="https://mirror.invalid/core.gz"),
            "wrong tag": lambda r: r.update(tag_name="mihomo-smart"),
            "draft": lambda r: r.update(draft=True),
            "wrong release": lambda r: r.update(html_url="https://github.com/msm9527/mosdns/releases/tag/mihomo-smart"),
            "invalid date": lambda r: r["assets"][0].update(updated_at="yesterday"),
        }
        for name, mutate in cases.items():
            with self.subTest(name=name):
                release = self.release()
                mutate(release)
                with self.assertRaises(ValueError):
                    mirror.select_assets(release)

    def test_rejects_mixed_source_revisions(self):
        release = self.release()
        asset = release["assets"][0]
        asset["name"] = asset["name"].replace("6a80483", "abcdef0")
        asset["browser_download_url"] = mirror.ASSET_URL + asset["name"]
        with self.assertRaisesRegex(ValueError, "mixed revisions"):
            mirror.select_assets(release)

    def test_copies_original_bytes_and_creates_matching_manifest(self):
        release = self.release()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with mock.patch.object(mirror, "fetch_release", return_value=release), \
                    mock.patch.object(mirror.urllib.request, "urlopen", side_effect=lambda *_a, **_k: io.BytesIO(self.payload)) as request:
                result = mirror.prepare(root)
            self.assertEqual("alpha-smart-6a80483", result["version"])
            self.assertFalse(result["patched"])
            self.assertFalse(result["recompiled"])
            self.assertTrue(result["verified"])
            self.assertEqual(12, len(list(root.iterdir())))
            for asset in result["assets"]:
                self.assertEqual(self.payload, (root / asset["name"]).read_bytes())
            for call in request.call_args_list:
                self.assertNotIn("Authorization", call.args[0].headers)
            for line in (root / "SHA256SUMS").read_text().splitlines():
                digest, name = line.split("  ")
                self.assertEqual(digest, hashlib.sha256((root / name).read_bytes()).hexdigest())
            self.assertEqual(result, json.loads((root / "mihomo-smart-source.json").read_text()))

    def test_rejects_changed_or_truncated_download_without_publishing_file(self):
        asset = mirror.select_assets(self.release())[0]
        for payload in [b"short", b"x" * len(self.payload), self.payload + b"extra"]:
            with self.subTest(payload=payload), tempfile.TemporaryDirectory() as directory:
                with mock.patch.object(mirror.urllib.request, "urlopen", return_value=io.BytesIO(payload)):
                    with self.assertRaises(ValueError):
                        mirror.download_asset(asset, Path(directory))
                self.assertEqual([], list(Path(directory).iterdir()))

    def test_release_changes_during_download_do_not_create_provenance(self):
        first, changed = self.release(), self.release()
        for asset in changed["assets"]:
            asset["updated_at"] = "2026-11-18T17:34:27Z"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with mock.patch.object(mirror, "fetch_release", side_effect=[first, changed]), \
                    mock.patch.object(mirror.urllib.request, "urlopen", side_effect=lambda *_a, **_k: io.BytesIO(self.payload)):
                with self.assertRaisesRegex(ValueError, "changed during download"):
                    mirror.prepare(root)
            self.assertFalse((root / "mihomo-smart-source.json").exists())
            self.assertFalse((root / "SHA256SUMS").exists())

    def test_refuses_to_mix_old_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            (Path(directory) / "old-patched.gz").write_bytes(b"keep")
            with mock.patch.object(mirror, "fetch_release") as request:
                with self.assertRaisesRegex(ValueError, "must be empty"):
                    mirror.prepare(Path(directory))
            request.assert_not_called()


if __name__ == "__main__":
    unittest.main()
