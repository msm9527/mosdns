# Sing-Box native release checks

The stable and testing channels resolve their upstream branch to an immutable
commit before building. Binary versions and release metadata use that commit's
version plus `-msm.1`. All platforms in one channel share the same source commit
and patch set. A native Linux runtime failure prevents publishing either channel.

`apply-patches.sh` runs inside the upstream source checkout and applies:

- `clash-api-context.patch`: API requests inherit the instance's certificate
  roots and measurement policy while retaining request/lifecycle cancellation.
- `shadowsocks-udp-family.patch`: Shadowsocks server NAT sessions separate IPv4,
  IPv6 and unresolved hostnames, preserving multiple targets within one family.
  The script copies the pinned dependency before patching it; it never changes
  the shared Go module cache.

The native repairs do not change the global dialer's interface binding,
source-address selection, route marks or firewall policy. The dependency patch
requires its exact reviewed module version. An upstream dependency change must
be reviewed and validated before updating that pin.

The CI baseline runs the instance-context and UDP tests, including the race
detector. `verify_singbox_runtime.py` then runs the compiled binary against local
HTTP/HTTPS/DNS/UDP fixtures and a real encrypted Shadowsocks relay. It verifies:

- remote provider loading, selection, updates and failed-update preservation;
- rule/direct/global forwarding and IPv4/IPv6 domain and literal destinations;
- TCP/UDP DNS, both FakeIP families and encrypted UDP forwarding;
- one UDP association alternating both families and multiple targets;
- instance-configured certificate trust and rejection when that trust is removed;
- rejected invalid reloads, valid hot reloads, full restarts and persistent state;
- concurrent payload integrity and local throughput.

Run the native gate from this repository with a new private working directory:

```sh
python3 scripts/verify_singbox_runtime.py /path/to/sing-box \
  --work-dir /tmp/singbox-runtime-new
```

Python 3, OpenSSL and working loopback IPv4/IPv6 are required. The gate uses no
external network, TUN device, host route, firewall modification or production
credentials. It writes native configurations, process logs and a successful
`runtime-result.json` into that directory. Throughput measures the local fixture,
not an Internet connection. `source.json` records source, dependency and patch
identities, and `SHA256SUMS` covers every published platform archive.
