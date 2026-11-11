#!/usr/bin/env python3
"""Offline release gate: real reF1nd providers, encrypted forwarding and reloads.

Only loopback sockets and a private work directory are used. No external DNS,
subscription, credentials, TUN device, firewall or host routes are required.
"""

import argparse
import concurrent.futures
import hashlib
import http.client
import http.server
import ipaddress
import json
from pathlib import Path
import socket
import socketserver
import ssl
import struct
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from singbox.runtime_udp import verify_udp_families

PAYLOAD = b"singbox-ref1nd-runtime-gate\n" * 32768
PORTS = set()


def require(value, message):
    if not value:
        raise RuntimeError(message)


def port():
    for _ in range(30):
        with socket.socket() as tcp, socket.socket(type=socket.SOCK_DGRAM) as udp:
            tcp.bind(("127.0.0.1", 0))
            value = tcp.getsockname()[1]
            if value in PORTS:
                continue
            try:
                udp.bind(("127.0.0.1", value))
            except OSError:
                continue
            PORTS.add(value)
            return value
    raise RuntimeError("could not reserve a free TCP/UDP fixture port")


def read_exact(sock, count):
    result = b""
    while len(result) < count:
        data = sock.recv(count - len(result))
        require(data, "unexpected EOF")
        result += data
    return result


def address(host, target_port):
    try:
        ip = ipaddress.ip_address(host)
        raw = bytes([1 if ip.version == 4 else 4]) + ip.packed
    except ValueError:
        raw = bytes([3, len(host)]) + host.encode()
    return raw + struct.pack("!H", target_port)


def socks(proxy_port, target, target_port, command=1):
    sock = socket.create_connection(("127.0.0.1", proxy_port), timeout=4)
    try:
        sock.sendall(b"\x05\x01\x00")
        require(read_exact(sock, 2) == b"\x05\x00", "SOCKS authentication failed")
        sock.sendall(bytes([5, command, 0]) + address(target, target_port))
        header = read_exact(sock, 4)
        require(header[1] == 0, f"SOCKS request rejected: {header!r}")
        size = {1: 4, 4: 16}.get(header[3])
        if size is None:
            size = read_exact(sock, 1)[0]
        relay = read_exact(sock, size)
        relay_port = struct.unpack("!H", read_exact(sock, 2))[0]
        return sock, relay, relay_port
    except Exception:
        sock.close()
        raise


def fetch(proxy_port, host, target_port):
    sock, _, _ = socks(proxy_port, host, target_port)
    with sock:
        sock.sendall(b"GET /payload HTTP/1.1\r\nHost: fixture.test\r\nConnection: close\r\n\r\n")
        response = http.client.HTTPResponse(sock)
        response.begin()
        body = response.read()
        require(response.status == 200 and hashlib.sha256(body).digest() == hashlib.sha256(PAYLOAD).digest(),
                f"payload corrupted through {host}: status={response.status}, bytes={len(body)}")


class HTTP6(http.server.ThreadingHTTPServer):
    address_family = socket.AF_INET6


class UDP6(socketserver.ThreadingUDPServer):
    address_family = socket.AF_INET6


class Fixtures:
    def __init__(self):
        self.provider, self.provider_hits, self.dns_hits, self.servers = b"", 0, 0, []
        self.udp_sources = {}

    def serve(self, server):
        self.servers.append(server)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        return server.server_address[1]

    def start(self, directory):
        fixture = self

        class HTTP(http.server.BaseHTTPRequestHandler):
            def do_HEAD(self):
                self.send_response(204)
                self.end_headers()

            def do_GET(self):
                if self.path == "/provider":
                    fixture.provider_hits += 1
                    body = fixture.provider
                else:
                    body = PAYLOAD
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_):
                pass

        class DNS(socketserver.BaseRequestHandler):
            def handle(self):
                packet, sock = self.request
                kind = struct.unpack("!H", packet[-4:-2])[0]
                fixture.dns_hits += 1
                label = packet[13:13 + packet[12]].decode()
                if label.endswith("28") and kind == 1 or label.endswith("1") and kind == 28:
                    sock.sendto(packet[:2] + struct.pack("!HHHHH", 0x8180, 1, 0, 0, 0) + packet[12:], self.client_address)
                    return
                ip = "::1" if kind == 28 else "127.0.0.1"
                data = ipaddress.ip_address(ip).packed
                answer = b"\xc0\x0c" + struct.pack("!HHIH", kind, 1, 30, len(data)) + data
                sock.sendto(packet[:2] + struct.pack("!HHHHH", 0x8180, 1, 1, 0, 0) + packet[12:] + answer, self.client_address)

        class Echo(socketserver.BaseRequestHandler):
            def handle(self):
                data, sock = self.request
                fixture.udp_sources[data] = self.client_address[1]
                sock.sendto(b"encrypted-echo:" + data, self.client_address)

        self.http4 = self.serve(http.server.ThreadingHTTPServer(("127.0.0.1", 0), HTTP))
        self.http6 = self.serve(HTTP6(("::1", 0), HTTP))
        certificate, key = directory / "fixture.crt", directory / "fixture.key"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                        "-subj", "/CN=singbox-runtime-fixture", "-addext", "subjectAltName=IP:127.0.0.1",
                        "-keyout", str(key), "-out", str(certificate)], check=True, capture_output=True, timeout=20)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certificate, key)
        https = http.server.ThreadingHTTPServer(("127.0.0.1", 0), HTTP)
        https.socket = context.wrap_socket(https.socket, server_side=True)
        self.https = self.serve(https)
        self.dns = self.serve(socketserver.ThreadingUDPServer(("127.0.0.1", 0), DNS))
        self.udp4 = self.serve(socketserver.ThreadingUDPServer(("127.0.0.1", 0), Echo))
        self.udp6 = self.serve(UDP6(("::1", 0), Echo))
        self.udp4_second = self.serve(socketserver.ThreadingUDPServer(("127.0.0.1", 0), Echo))
        self.udp6_second = self.serve(UDP6(("::1", 0), Echo))
    def close(self):
        for server in self.servers:
            server.shutdown()
            server.server_close()


def dns_query(target_port, domain, kind, tcp=False):
    name = b"".join(bytes([len(label)]) + label.encode() for label in domain.split(".")) + b"\x00"
    question = name + struct.pack("!HH", kind, 1)
    packet = struct.pack("!HHHHHH", 0x4D53, 0x100, 1, 0, 0, 0) + question
    with socket.socket(type=socket.SOCK_STREAM if tcp else socket.SOCK_DGRAM) as sock:
        sock.settimeout(4)
        sock.connect(("127.0.0.1", target_port))
        if tcp:
            sock.sendall(struct.pack("!H", len(packet)) + packet)
            result = read_exact(sock, struct.unpack("!H", read_exact(sock, 2))[0])
        else:
            sock.send(packet)
            result = sock.recv(4096)
    require(result[:2] == packet[:2] and result[3] & 15 == 0 and struct.unpack("!H", result[6:8])[0] == 1,
            f"DNS failed: {result.hex()}")
    offset = 12 + len(question)
    if result[offset] & 0xC0 == 0xC0:
        offset += 2
    else:
        while result[offset]:
            offset += result[offset] + 1
        offset += 1
    record_type, _, _, size = struct.unpack("!HHIH", result[offset:offset + 10])
    require(record_type == kind, f"unexpected DNS type {record_type}")
    return str(ipaddress.ip_address(result[offset + 10:offset + 10 + size]))


class Gate:
    def __init__(self, binary, directory):
        self.binary, self.directory = str(Path(binary).resolve()), directory
        self.processes, self.logs, self.results = [], [], []
        self.fixtures = Fixtures()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def write(self, name, config):
        path = self.directory / name
        path.write_text(json.dumps(config))
        checked = subprocess.run([self.binary, "check", "-c", str(path)], capture_output=True, text=True, timeout=20)
        require(checked.returncode == 0, f"native config check failed: {checked.stdout}{checked.stderr}")
        return path

    def start(self, name, config):
        log = (self.directory / (name + ".log")).open("a")
        self.logs.append(log)
        process = subprocess.Popen([self.binary, "run", "-c", str(config)], cwd=self.directory, stdout=log, stderr=log)
        self.processes.append(process)
        return process

    def stop(self, process):
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise RuntimeError("core failed graceful shutdown")
        require(process.returncode == 0, f"core exited unexpectedly: {process.returncode}")

    def api(self, path, method="GET", body=None, expected=200):
        request = urllib.request.Request(f"http://127.0.0.1:{self.controller}{path}", method=method,
                                         data=json.dumps(body).encode() if body is not None else None,
                                         headers={"Content-Type": "application/json"})
        try:
            response = self.opener.open(request, timeout=8)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            require(response.status == expected, f"{method} {path}: {response.status}: {data!r}")
            return json.loads(data) if data else None

    def ready(self):
        deadline, last = time.monotonic() + 15, None
        while time.monotonic() < deadline:
            try:
                self.api("/version")
                self.api("/proxies/airport%2Fnode2")
                fetch(self.mixed, "127.0.0.1", self.fixtures.http4)
                return
            except (OSError, RuntimeError, http.client.HTTPException) as error:
                last = error
                time.sleep(0.1)
        raise RuntimeError(f"core never became ready: {last}")

    def run(self):
        self.fixtures.start(self.directory)
        f = self.fixtures
        self.controller, self.mixed, self.dns_port, ss_port = port(), port(), port(), port()
        resolver = {"type": "udp", "tag": "fixture", "server": "127.0.0.1", "server_port": f.dns}
        server_config = self.write("server.json", {
            "log": {"level": "info"}, "dns": {"servers": [resolver], "final": "fixture"},
            "inbounds": [{"type": "shadowsocks", "tag": "encrypted-server", "listen": "127.0.0.1",
                          "listen_port": ss_port, "method": "aes-128-gcm", "password": "isolated-runtime-fixture"}],
            "outbounds": [{"type": "direct", "tag": "direct"}],
            "route": {"final": "direct", "default_domain_resolver": "fixture"}})
        self.start("server", server_config)

        def subscription(count):
            return json.dumps({"outbounds": [{"type": "shadowsocks", "tag": f"node{i}", "server": "127.0.0.1",
                "server_port": ss_port, "method": "aes-128-gcm", "password": "isolated-runtime-fixture"}
                for i in range(1, count + 1)]}).encode()

        f.provider = subscription(2)
        config = {"log": {"level": "warn"}, "certificate": {"certificate_path": str(self.directory / "fixture.crt")},
            "experimental": {"cache_file": {"enabled": True, "path": str(self.directory / "cache.db"), "store_fakeip": True},
                             "clash_api": {"external_controller": f"127.0.0.1:{self.controller}", "default_mode": "rule"}},
            "dns": {"servers": [resolver, {"type": "fakeip", "tag": "fake", "inet4_range": "198.18.0.0/15",
                                           "inet6_range": "fd42:4242:4242::/48"}],
                    "rules": [{"inbound": "in-dns", "server": "fake"}], "final": "fixture"},
            "inbounds": [{"type": "mixed", "tag": "mixed", "listen": "127.0.0.1", "listen_port": self.mixed},
                         {"type": "direct", "tag": "in-dns", "listen": "127.0.0.1", "listen_port": self.dns_port}],
            "providers": [{"type": "remote", "tag": "airport", "url": f"http://127.0.0.1:{f.http4}/provider",
                           "path": str(self.directory / "provider.json"), "update_interval": "1h",
                           "http_client": {"detour": "direct"}}],
            "outbounds": [{"type": "direct", "tag": "direct", "domain_resolver": "fixture"},
                          {"type": "selector", "tag": "select", "outbounds": ["direct"], "providers": ["airport"]},
                          {"type": "urltest", "tag": "auto", "providers": ["airport"],
                           "url": f"https://127.0.0.1:{f.https}/generate_204", "interval": "1m"}],
            "route": {"default_domain_resolver": "fixture", "rules": [
                {"inbound": "in-dns", "action": "hijack-dns"},
                {"clash_mode": "direct", "outbound": "direct"},
                {"clash_mode": "global", "outbound": "select"}], "final": "select"}}
        path = self.write("client.json", config)
        process = self.start("client", path)
        self.ready()
        require(len(self.api("/providers/proxies/airport")["proxies"]) == 2 and f.provider_hits > 0,
                "remote provider did not load its actual nodes")
        self.api("/proxies/select", "PUT", {"name": "airport/node2"}, 204)
        for mode in ("rule", "direct", "global"):
            self.api("/configs", "PATCH", {"mode": mode}, 204)
            for host, origin in (("127.0.0.1", f.http4), ("::1", f.http6), ("origin.fixture.test", f.http4)):
                fetch(self.mixed, host, origin)
        mappings = {}
        for name in ("first", "second"):
            for kind in (1, 28):
                domain = f"{name}{kind}.fixture.test"
                value = dns_query(self.dns_port, domain, kind)
                pool = "198.18.0.0/15" if kind == 1 else "fd42:4242:4242::/48"
                require(ipaddress.ip_address(value) in ipaddress.ip_network(pool), f"FakeIP outside pool: {value}")
                require(dns_query(self.dns_port, domain, kind, True) == value, "TCP/UDP DNS mappings disagree")
                fetch(self.mixed, value, f.http4 if kind == 1 else f.http6)
                mappings[(domain, kind)] = value
        for host, origin in ((mappings[("first1.fixture.test", 1)], f.udp4),
                             (mappings[("first28.fixture.test", 28)], f.udp6)):
            control, _, relay_port = socks(self.mixed, "0.0.0.0", 0, 3)
            with control, socket.socket(type=socket.SOCK_DGRAM) as packet:
                packet.settimeout(5)
                packet.sendto(b"\x00\x00\x00" + address(host, origin) + b"gate", ("127.0.0.1", relay_port))
                require(packet.recv(4096).endswith(b"encrypted-echo:gate"), f"encrypted UDP/FakeIP failed: {host}")
        delay_url = urllib.parse.urlencode({"url": f"https://127.0.0.1:{f.https}/generate_204", "timeout": 5000})
        verify_udp_families(self, socks, address, require)
        self.api("/proxies/" + urllib.parse.quote("airport/node2", safe="") + "/delay?" + delay_url)
        f.provider = b"invalid subscription"
        self.api("/providers/proxies/airport", "PUT", expected=503)
        fetch(self.mixed, "127.0.0.1", f.http4)
        f.provider = subscription(3)
        self.api("/providers/proxies/airport", "PUT", expected=204)
        require(len(self.api("/providers/proxies/airport")["proxies"]) == 3, "provider update did not apply")
        path.write_text('{"invalid_runtime_fixture":true}')
        self.api("/configs", "PUT", {}, 400)
        require(process.poll() is None, "invalid reload terminated the active process")
        fetch(self.mixed, "127.0.0.1", f.http4)
        path.write_text(json.dumps(config))
        self.api("/configs", "PUT", {}, 204)
        self.ready()
        require(process.poll() is None, "hot reload exited the process")
        path.write_text(json.dumps({key: value for key, value in config.items() if key != "certificate"}))
        self.api("/configs", "PUT", {}, 204)
        self.ready()
        self.api("/proxies/" + urllib.parse.quote("airport/node2", safe="") + "/delay?" + delay_url, expected=503)
        path.write_text(json.dumps(config))
        self.api("/configs", "PUT", {}, 204)
        self.ready()
        self.api("/proxies/" + urllib.parse.quote("airport/node2", safe="") + "/delay?" + delay_url)
        self.stop(process)
        process = self.start("client-restart", path)
        self.ready()
        require(self.api("/proxies/select")["now"] == "airport/node2", "selector was not persisted across restart")
        for (domain, kind), value in reversed(list(mappings.items())):
            require(dns_query(self.dns_port, domain, kind) == value, "FakeIP mappings were not persisted across restart")
        started = time.monotonic()
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            list(pool.map(lambda _: fetch(self.mixed, "127.0.0.1", f.http4), range(8)))
        self.results.append({"gate": "provider-encrypted-forwarding-dns-lifecycle", "passed": True,
                             "local_payload_mib_per_second": round(len(PAYLOAD) * 8 / (1 << 20) / (time.monotonic() - started), 2),
                             "provider_fetches": f.provider_hits, "resolver_queries": f.dns_hits})
        self.stop(process)
        require("inbound/shadowsocks[encrypted-server]" in (self.directory / "server.log").read_text(),
                "no evidence of forwarding through the encrypted Shadowsocks listener")

    def close(self):
        for process in reversed(self.processes):
            if process.poll() is None:
                self.stop(process)
        for log in self.logs:
            log.close()
        self.fixtures.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("--work-dir", type=Path, required=True)
    args = parser.parse_args()
    args.work_dir.mkdir(parents=True, exist_ok=False, mode=0o700)
    gate = Gate(args.binary, args.work_dir.resolve())
    try:
        gate.run()
    finally:
        gate.close()
    result = {"binary_sha256": hashlib.sha256(Path(gate.binary).read_bytes()).hexdigest(), "checks": gate.results}
    (gate.directory / "runtime-result.json").write_text(json.dumps(result, indent=2))
    print(json.dumps(result))


if __name__ == "__main__":
    main()
