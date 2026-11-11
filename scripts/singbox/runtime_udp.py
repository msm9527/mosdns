"""Real encrypted UDP association regressions used by the release gate."""

import socket


def verify_udp_families(gate, socks, address, require):
    """IPv4-first and IPv6-first associations must allow mixed destinations.

    One SOCKS source socket and one upstream encrypted UDP source socket stay
    alive throughout each sequence. On an unpatched relay, IPv4 first pins the
    server NAT entry to udp4 and every subsequent IPv6 packet times out.
    """
    fixture = gate.fixtures
    destinations = [
        ("127.0.0.1", fixture.udp4), ("::1", fixture.udp6),
        ("127.0.0.1", fixture.udp4_second), ("::1", fixture.udp6_second),
    ]
    for reverse in (False, True):
        source_ports = {}
        control, _, relay_port = socks(gate.mixed, "0.0.0.0", 0, 3)
        with control, socket.socket(type=socket.SOCK_DGRAM) as packet:
            packet.settimeout(4)
            targets = list(reversed(destinations)) if reverse else destinations
            for sequence, (host, target) in enumerate(targets * 4):
                payload = f"mixed-family-{reverse}-{sequence}".encode()
                packet.sendto(b"\x00\x00\x00" + address(host, target) + payload, ("127.0.0.1", relay_port))
                response = packet.recv(4096)
                require(response.endswith(b"encrypted-echo:" + payload), f"cross-family UDP corrupted: {host}")
                family = 6 if ":" in host else 4
                source_port = fixture.udp_sources[payload]
                require(source_ports.get(family, source_port) == source_port,
                        "same-family destinations lost their shared relay UDP source port")
                source_ports[family] = source_port
    gate.results.append({"gate": "encrypted-udp-one-association-multiple-families", "packets": 32, "passed": True})
