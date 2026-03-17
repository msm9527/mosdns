#!/usr/bin/env python3
import argparse
import json
import logging
import os
import shutil
import subprocess
import tempfile
import zipfile
from pathlib import Path

PROJECT_NAME = "mosdns"
ROOT_DIR = Path(__file__).resolve().parent
DEFAULT_RELEASE_DIR = ROOT_DIR / "release"
BUILD_ENV_KEYS = ("GOOS", "GOARCH", "GOAMD64", "GOARM", "GOMIPS", "GOMIPS64")

logger = logging.getLogger(__name__)

TARGETS = [
    {"name": "android-arm64", "display": "android/arm64", "env": {"GOOS": "android", "GOARCH": "arm64"}},
    {"name": "darwin-amd64", "display": "darwin/amd64", "env": {"GOOS": "darwin", "GOARCH": "amd64"}},
    {"name": "darwin-arm64", "display": "darwin/arm64", "env": {"GOOS": "darwin", "GOARCH": "arm64"}},
    {"name": "windows-amd64", "display": "windows/amd64", "env": {"GOOS": "windows", "GOARCH": "amd64"}},
    {
        "name": "windows-amd64-v3",
        "display": "windows/amd64 (GOAMD64=v3)",
        "env": {"GOOS": "windows", "GOARCH": "amd64", "GOAMD64": "v3"},
    },
    {"name": "windows-arm64", "display": "windows/arm64", "env": {"GOOS": "windows", "GOARCH": "arm64"}},
    {"name": "linux-amd64", "display": "linux/amd64", "env": {"GOOS": "linux", "GOARCH": "amd64"}},
    {
        "name": "linux-amd64-v3",
        "display": "linux/amd64 (GOAMD64=v3)",
        "env": {"GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v3"},
    },
    {"name": "linux-arm64", "display": "linux/arm64", "env": {"GOOS": "linux", "GOARCH": "arm64"}},
    {"name": "linux-armv5", "display": "linux/arm/v5", "env": {"GOOS": "linux", "GOARCH": "arm", "GOARM": "5"}},
    {"name": "linux-armv6", "display": "linux/arm/v6", "env": {"GOOS": "linux", "GOARCH": "arm", "GOARM": "6"}},
    {"name": "linux-armv7", "display": "linux/arm/v7", "env": {"GOOS": "linux", "GOARCH": "arm", "GOARM": "7"}},
]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("-i", "--index", type=int)
    parser.add_argument("--github-matrix", action="store_true")
    parser.add_argument("--release-dir", default=str(DEFAULT_RELEASE_DIR))
    return parser.parse_args()


def read_version_from_file(path: Path) -> str:
    try:
        with path.open("r", encoding="utf-8") as handle:
            for line in handle:
                value = line.strip()
                if value and not value.startswith("#"):
                    return value
    except OSError:
        return ""
    return ""


def resolve_version() -> str:
    version = os.getenv("VERSION", "").strip()
    if version:
        return version

    version = read_version_from_file(ROOT_DIR / ".version")
    if version:
        return version

    try:
        output = subprocess.check_output(
            ["git", "describe", "--tags", "--abbrev=0"],
            cwd=ROOT_DIR,
            text=True,
        )
        return output.strip()
    except subprocess.CalledProcessError:
        return "dev"


def archive_filename(target: dict) -> str:
    parts = [PROJECT_NAME]
    for key in BUILD_ENV_KEYS:
        value = target["env"].get(key, "").strip()
        if value:
            parts.append(value)
    return "-".join(parts) + ".zip"


def matrix_payload() -> str:
    include = []
    for index, target in enumerate(TARGETS):
        include.append(
            {
                "index": index,
                "name": target["name"],
                "display": target["display"],
                "artifact": archive_filename(target),
            }
        )
    return json.dumps({"include": include}, ensure_ascii=False)


def git_tracked_config_files() -> list[Path]:
    output = subprocess.check_output(
        ["git", "ls-files", "config"],
        cwd=ROOT_DIR,
        text=True,
    )
    files = []
    for line in output.splitlines():
        item = line.strip()
        if item:
            files.append(Path(item))
    if not files:
        raise RuntimeError("no tracked config files found under config/")
    return files


def run_command(cmd: list[str], env: dict[str, str] | None = None) -> None:
    subprocess.run(cmd, cwd=ROOT_DIR, env=env, check=True)


def ensure_release_dir(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True)


def add_text_file_from_root(zf: zipfile.ZipFile, relative_path: str) -> None:
    source = ROOT_DIR / relative_path
    zf.write(source, relative_path)


def package_release(target: dict, version: str, release_dir: Path, config_files: list[Path]) -> Path:
    env = os.environ.copy()
    env.update(target["env"])

    suffix = ".exe" if target["env"]["GOOS"] == "windows" else ""
    archive_name = archive_filename(target)
    artifact_path = release_dir / archive_name
    artifact_path.unlink(missing_ok=True)

    with tempfile.TemporaryDirectory(prefix=f"{PROJECT_NAME}-release-") as temp_dir:
        stage_dir = Path(temp_dir)
        binary_name = PROJECT_NAME + suffix
        binary_path = stage_dir / binary_name

        run_command(
            [
                "go",
                "build",
                "-ldflags",
                f"-s -w -X main.version={version}",
                "-trimpath",
                "-o",
                str(binary_path),
                str(ROOT_DIR),
            ],
            env=env,
        )

        try:
            with zipfile.ZipFile(
                artifact_path,
                mode="w",
                compression=zipfile.ZIP_DEFLATED,
                compresslevel=5,
            ) as zf:
                zf.write(binary_path, binary_name)
                add_text_file_from_root(zf, "README.md")
                add_text_file_from_root(zf, "LICENSE")
                for config_file in config_files:
                    zf.write(ROOT_DIR / config_file, config_file.as_posix())
        except Exception:
            artifact_path.unlink(missing_ok=True)
            raise

    return artifact_path


def build_targets(selected_targets: list[dict], release_dir: Path) -> None:
    version = resolve_version()
    config_files = git_tracked_config_files()
    ensure_release_dir(release_dir)

    logger.info("building %s target(s) with version %s", len(selected_targets), version)
    for target in selected_targets:
        logger.info("building %s", target["display"])
        artifact_path = package_release(target, version, release_dir, config_files)
        logger.info("packaged %s", artifact_path.name)


def main() -> int:
    args = parse_args()
    if args.github_matrix:
        print(matrix_payload())
        return 0

    if args.index is None:
        selected_targets = TARGETS
    else:
        if args.index < 0 or args.index >= len(TARGETS):
            raise SystemExit(f"target index {args.index} is out of range")
        selected_targets = [TARGETS[args.index]]

    release_dir = Path(args.release_dir).resolve()
    build_targets(selected_targets, release_dir)

    if release_dir == DEFAULT_RELEASE_DIR:
        audit_dir = release_dir / "audit_logs"
        if audit_dir.exists() and not any(audit_dir.iterdir()):
            shutil.rmtree(audit_dir)
    return 0


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO)
    raise SystemExit(main())
