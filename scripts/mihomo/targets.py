"""Packaging names must identify the CPU baseline that was actually compiled."""


def allowed_targets(channel):
    targets = {}
    for os_name in (("linux", "darwin") if channel == "smart" else ("linux", "windows", "darwin")):
        base = f"{os_name}-amd64"
        targets[base + ("-v1" if channel == "smart" else "")] = {
            "GOOS": os_name, "GOARCH": "amd64", "GOAMD64": "v1",
        }
        targets[base + ("-v3" if channel == "smart" else "v3")] = {
            "GOOS": os_name, "GOARCH": "amd64", "GOAMD64": "v3",
        }
        targets[f"{os_name}-arm64"] = {"GOOS": os_name, "GOARCH": "arm64"}
    for arm in ("5", "6", "7"):
        targets[f"linux-armv{arm}"] = {"GOOS": "linux", "GOARCH": "arm", "GOARM": arm}
    if channel == "smart":
        targets["linux-386"] = {"GOOS": "linux", "GOARCH": "386"}
    else:
        for arch, float_mode in (("mips", "softfloat"), ("mipsle", "softfloat"), ("mipsle", "hardfloat")):
            targets[f"linux-{arch}-{float_mode}"] = {
                "GOOS": "linux", "GOARCH": arch, "GOMIPS": float_mode,
            }
    return targets


def validate_target(channel, suffix, package_format, environment):
    if channel not in ("meta", "alpha", "smart"):
        raise ValueError(f"unsupported channel: {channel}")
    target = allowed_targets(channel).get(suffix)
    if target is None:
        raise ValueError(f"unsupported asset platform: {channel}/{suffix}")
    for key, expected in target.items():
        if environment.get(key) != expected:
            raise ValueError(f"asset {suffix} requires {key}={expected}, got {environment.get(key)!r}")
    expected_format = "gz" if channel == "smart" else "exe" if target["GOOS"] == "windows" else "tar.gz"
    if package_format != expected_format:
        raise ValueError(f"asset {suffix} requires package format {expected_format}")
    if environment.get("CGO_ENABLED") != "0":
        raise ValueError("release artifacts require CGO_ENABLED=0 for system compatibility")
