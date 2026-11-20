# Sing-Box upstream source releases

Stable and testing releases build the author's unmodified `reF1nd/sing-box`
branches. Each channel resolves its branch to one immutable commit, uses the
upstream version without an MSM suffix, and retains the upstream build tags,
linker settings and dependency files. The author currently provides no GitHub
release binaries, so these are source builds rather than mirrored binaries.

`build-upstream.sh` rejects a dirty checkout, builds outside the source tree with
read-only module resolution, verifies the module cache, and checks the embedded
source commit and `vcs.modified=false`. It records the Go build information,
build tags and hashes of the unchanged `go.mod` and `go.sum` for every platform.
The release contains this evidence in `source.json` and archive checksums in
`SHA256SUMS`. Uploaded files are verified before the release metadata is updated.

The Linux release gate runs `verify_singbox_runtime.py` against local fixtures:

- remote provider loading, selection, updates and failed-update preservation;
- rule/direct/global forwarding and IPv4/IPv6 domain and literal destinations;
- TCP/UDP DNS, both FakeIP families and encrypted UDP forwarding;
- the HTTPS delay API with a process-scoped fixture certificate, rejected invalid reloads and valid hot reloads;
- full restarts, persistent selection/FakeIP state and concurrent payload integrity.

Run the gate with a new private working directory:

```sh
python3 scripts/verify_singbox_runtime.py /path/to/sing-box \
  --work-dir /tmp/singbox-runtime-new
```

Python 3, OpenSSL and working loopback IPv4/IPv6 are required. The gate uses no
external network, production subscription, TUN device, host route or firewall
modification. Logs and `runtime-result.json` are written to the work directory.
Throughput describes only the local fixture.

The historical `apply-patches.sh` and patch files are retained for diagnosis and
are **not invoked by the release workflow**. The old cross-family UDP association
and instance-specific TLS certificate regressions can be requested separately
with `--patch-regressions`; they may fail on upstream builds and are not claimed
as passing release checks. Release failures must be investigated in MSM's
integration or reported upstream rather than repaired by patching the core.
