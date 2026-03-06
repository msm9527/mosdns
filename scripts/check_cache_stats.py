#!/usr/bin/env python3
"""Check cache runtime stats exposed by /plugins/{tag}/stats."""

from __future__ import annotations

import argparse
import json
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="检查 mosdns cache 插件的 /stats 运行态接口",
    )
    parser.add_argument(
        "--base-url",
        default="http://127.0.0.1:9099",
        help="API 基地址，默认: http://127.0.0.1:9099",
    )
    parser.add_argument(
        "--config",
        default="config/sub_config/cache.yaml",
        help="缓存配置文件路径，默认: config/sub_config/cache.yaml",
    )
    parser.add_argument(
        "--tag",
        action="append",
        dest="tags",
        default=[],
        help="指定要检查的 cache tag，可重复传入；未指定时从配置文件自动提取",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=3.0,
        help="单个请求超时秒数，默认: 3",
    )
    parser.add_argument(
        "--require-wal",
        action="store_true",
        help="要求所有目标实例都启用 wal_file",
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="若发现 last_dump / last_load / last_wal_replay 为 error 则返回非零退出码",
    )
    parser.add_argument(
        "--show-json",
        action="store_true",
        help="同时输出原始 JSON",
    )
    return parser.parse_args()


def load_tags(config_path: Path) -> list[str]:
    if not config_path.exists():
        raise FileNotFoundError(f"配置文件不存在: {config_path}")

    tags: list[str] = []
    current_tag = ""
    pending_cache = False

    for raw_line in config_path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        tag_match = re.match(r"-\s*tag:\s*(\S+)", line)
        if tag_match:
            current_tag = tag_match.group(1).strip("\"'")
            pending_cache = False
            continue
        type_match = re.match(r"type:\s*(\S+)", line)
        if type_match and current_tag:
            pending_cache = type_match.group(1).strip("\"'") == "cache"
            if pending_cache:
                tags.append(current_tag)
            current_tag = ""
            pending_cache = False

    return tags


def fetch_json(base_url: str, tag: str, timeout: float) -> dict[str, Any]:
    url = urllib.parse.urljoin(base_url.rstrip("/") + "/", f"plugins/{tag}/stats")
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        charset = resp.headers.get_content_charset() or "utf-8"
        body = resp.read().decode(charset)
    data = json.loads(body)
    if not isinstance(data, dict):
        raise ValueError(f"{tag}: /stats 返回不是对象")
    return data


def status_line(label: str, payload: dict[str, Any]) -> str:
    status = payload.get("status") or "unknown"
    at = payload.get("at") or "-"
    entries = payload.get("entries")
    err = payload.get("error") or ""
    details = [status, at]
    if entries not in (None, ""):
        details.append(f"entries={entries}")
    if err:
        details.append(f"error={err}")
    return f"{label}: " + " | ".join(str(part) for part in details)


def counter(payload: dict[str, Any], key: str) -> int:
    value = payload.get("counters", {}).get(key, 0)
    return int(value) if isinstance(value, (int, float)) else 0


def summarize(tag: str, payload: dict[str, Any]) -> list[str]:
    lines = [f"[{tag}]"]
    query_total = counter(payload, "query_total")
    hit_total = counter(payload, "hit_total")
    lazy_hit_total = counter(payload, "lazy_hit_total")
    lazy_update_total = counter(payload, "lazy_update_total")
    dropped_total = counter(payload, "lazy_update_dropped_total")
    hit_rate = (hit_total / query_total * 100.0) if query_total else 0.0

    lines.append(
        "  "
        + " | ".join(
            [
                f"snapshot={payload.get('snapshot_file') or '-'}",
                f"wal={payload.get('wal_file') or '-'}",
                f"l1={payload.get('l1_size', 0)}",
                f"l2={payload.get('backend_size', 0)}",
                f"updated_keys={payload.get('updated_keys', 0)}",
            ]
        )
    )
    lines.append(
        "  "
        + " | ".join(
            [
                f"query_total={query_total}",
                f"hit_total={hit_total}",
                f"hit_rate={hit_rate:.2f}%",
                f"lazy_hit_total={lazy_hit_total}",
                f"lazy_update_total={lazy_update_total}",
                f"lazy_update_dropped_total={dropped_total}",
            ]
        )
    )
    lines.append("  " + status_line("last_dump", payload.get("last_dump", {})))
    lines.append("  " + status_line("last_load", payload.get("last_load", {})))
    lines.append("  " + status_line("last_wal_replay", payload.get("last_wal_replay", {})))
    return lines


def detect_issues(
    tag: str,
    payload: dict[str, Any],
    require_wal: bool,
    strict: bool,
) -> list[str]:
    issues: list[str] = []
    wal_file = payload.get("wal_file") or ""
    if require_wal and not wal_file:
        issues.append(f"{tag}: 未启用 wal_file")

    if strict:
        for op_key in ("last_dump", "last_load", "last_wal_replay"):
            op = payload.get(op_key, {})
            if isinstance(op, dict) and op.get("status") == "error":
                issues.append(f"{tag}: {op_key} = error ({op.get('error') or 'unknown'})")

    return issues


def main() -> int:
    args = parse_args()
    config_path = Path(args.config)
    tags = args.tags or load_tags(config_path)
    if not tags:
        print("未找到可检查的 cache tag", file=sys.stderr)
        return 2

    issues: list[str] = []
    for tag in tags:
        try:
            payload = fetch_json(args.base_url, tag, args.timeout)
        except FileNotFoundError as exc:
            print(str(exc), file=sys.stderr)
            return 2
        except urllib.error.HTTPError as exc:
            issues.append(f"{tag}: HTTP {exc.code}")
            continue
        except urllib.error.URLError as exc:
            issues.append(f"{tag}: 请求失败 ({exc.reason})")
            continue
        except json.JSONDecodeError as exc:
            issues.append(f"{tag}: JSON 解析失败 ({exc})")
            continue
        except Exception as exc:  # pragma: no cover - defensive fallback
            issues.append(f"{tag}: 未知错误 ({exc})")
            continue

        print("\n".join(summarize(tag, payload)))
        if args.show_json:
            print(json.dumps(payload, ensure_ascii=False, indent=2))
        issues.extend(detect_issues(tag, payload, args.require_wal, args.strict))

    if issues:
        print("\n发现问题:", file=sys.stderr)
        for issue in issues:
            print(f"- {issue}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
