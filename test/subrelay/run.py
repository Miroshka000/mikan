#!/usr/bin/env python3
"""Real HTTP-01 ACME issuance and two-edge outage test. Requires Docker and OpenSSL."""
import argparse
import http.client
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import time
import uuid

PEBBLE = "ghcr.io/letsencrypt/pebble@sha256:0c81f05db371f436c97fb894bd6d2ab8fc9c8e2ce40f8bef8a8e15941bcd75ef"
PYTHON = "python@sha256:9ef1a599f863231c3d91e941389bfdc4a2d6070557243d54df1a4e57015b960e"
TOKEN = "abcdefghijklmnopqrstuvwx"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="mikan-sub:ci")
    parser.add_argument("--break-http-01", action="store_true", help="negative control: certificate issuance must fail with HTTP-01 unreachable")
    args = parser.parse_args()
    prefix = "mikan-sub-test-" + uuid.uuid4().hex[:10]
    network = prefix + "-net"
    containers, volumes = [], []

    def docker(*args, **kwargs):
        return subprocess.check_output(["docker", *args], text=True, **kwargs).strip()

    def create(name, *arguments):
        containers.append(name)
        local_hosts = os.environ.get("NO_PROXY", "") + "," + os.environ.get("no_proxy", "") + ",pebble,panel.test,sub.test,127.0.0.1,localhost"
        docker("create", "--name", name, "-e", "NO_PROXY=" + local_hosts, "-e", "no_proxy=" + local_hosts, *arguments)

    def copy_fixture(name, source, target):
        docker("cp", str(source), name + ":" + target)

    def port(name, number):
        return int(docker("port", name, str(number)).rsplit(":", 1)[1])

    def fetch(number, context, path="/sub/" + TOKEN, name="sub.test", timeout=8):
        connection = http.client.HTTPConnection("127.0.0.1", number, timeout=timeout)
        raw = socket.create_connection(("127.0.0.1", number), timeout=timeout)
        connection.sock = context.wrap_socket(raw, server_hostname=name)
        try:
            connection.request("GET", path, headers={"Host": name, "User-Agent": "mihomo/ci", "X-Hwid": "ci-device", "Cookie": "must-not-reach-origin=1"})
            response = connection.getresponse()
            return response.status, dict(response.getheaders()), response.read()
        finally:
            connection.close()

    def until(action, seconds=90):
        deadline, last = time.monotonic() + seconds, None
        while time.monotonic() < deadline:
            try:
                return action()
            except (OSError, http.client.HTTPException, AssertionError) as error:
                last = error
                time.sleep(0.2)
        raise AssertionError("integration deadline exceeded") from last

    docker("network", "create", network)
    try:
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            cert, key = directory / "test-ca.pem", directory / "test-key.pem"
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key), "-out", str(cert), "-days", "1", "-subj", "/CN=mikan-sub-ci", "-addext", "subjectAltName=DNS:pebble,DNS:panel.test,IP:127.0.0.1", "-addext", "basicConstraints=critical,CA:TRUE"], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            # These are ephemeral test keys, used only inside this test network.
            cert.chmod(0o644)
            key.chmod(0o644)
            config = directory / "pebble.json"
            config.write_text(json.dumps({"pebble": {"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000", "certificate": "/tmp/test-ca.pem", "privateKey": "/tmp/test-key.pem", "httpPort": 81 if args.break_http_01 else 80, "tlsPort": 4443}}))
            config.chmod(0o644)
            ca = prefix + "-ca"
            create(ca, "--network", network, "--network-alias", "pebble", "-p", "127.0.0.1::15000", "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "-e", "PEBBLE_AUTHZREUSE=0", PEBBLE, "-config", "/tmp/pebble.json")
            docker("cp", str(directory), ca + ":/tmp")
            docker("start", ca)
            trust = ssl.create_default_context(cafile=str(cert))
            management = port(ca, 15000)
            def root_certificate():
                connection = http.client.HTTPSConnection("127.0.0.1", management, context=trust, timeout=5)
                try:
                    connection.request("GET", "/roots/0")
                    response = connection.getresponse()
                    assert response.status == 200
                    return response.read().decode()
                finally:
                    connection.close()
            root = until(root_certificate)
            issued_trust = ssl.create_default_context(cadata=root)
            trust_volume = prefix + "-trust"
            volumes.append(trust_volume)
            origin = prefix + "-origin"
            create(origin, "--network", network, "--network-alias", "panel.test", "-v", trust_volume + ":/trust", PYTHON, "python", "/tmp/panel.py")
            for fixture in (cert, key):
                copy_fixture(origin, fixture, "/tmp/" + fixture.name)
            copy_fixture(origin, Path(__file__).with_name("panel.py"), "/tmp/panel.py")
            docker("start", origin)
            copy_fixture(origin, cert, "/trust/test-ca.pem")
            edges = []
            for index in range(2):
                name, volume = prefix + "-edge-" + str(index), prefix + "-acme-" + str(index)
                volumes.append(volume)
                options = ["--network", network]
                if index == 0:
                    options += ["--network-alias", "sub.test"]
                create(name, *options, "--read-only", "--cap-drop=ALL", "--cap-add=NET_BIND_SERVICE", "-p", "127.0.0.1::443", "-v", volume + ":/data/acme", "-e", "MIKAN_SUB_TOKEN=ci-subscription-token", "-v", trust_volume + ":/test-ca:ro", "-e", "SSL_CERT_FILE=/test-ca/test-ca.pem", args.image, "-upstream", "https://panel.test:8443/api/subscription", "-path", "sub", "-domain", "sub.test", "-acme-directory", "https://pebble:14000/dir", "-failure-threshold", "1", "-retry-delay", "30s", "-metrics-listen", "127.0.0.1:9091")
                docker("start", name)
                edges.append((name, port(name, 443)))
            def profile(number):
                status, headers, body = fetch(number, issued_trust)
                assert status == 200 and body == b"proxies: []\n", (status, body)
                return headers
            if args.break_http_01:
                try:
                    until(lambda: profile(edges[0][1]), seconds=12)
                except AssertionError:
                    print("PASS: broken HTTP-01 cannot issue a certificate (TLS-ALPN is also unreachable)", flush=True)
                    return
                raise AssertionError("certificate issued despite broken HTTP-01 and TLS-ALPN")
            # Pebble uses an unreachable TLS-ALPN port (4443), forcing HTTP-01.
            # HTTP-01 must reach the instance currently issuing its own certificate.
            until(lambda: profile(edges[0][1]))
            print("PASS: first HTTP-01 certificate and trusted HTTPS", flush=True)
            docker("network", "disconnect", network, edges[0][0])
            docker("network", "connect", network, edges[0][0])
            docker("network", "disconnect", network, edges[1][0])
            docker("network", "connect", "--alias", "sub.test", network, edges[1][0])
            edges = [(name, port(name, 443)) for name, _ in edges]
            until(lambda: profile(edges[1][1]))
            print("PASS: second independent HTTP-01 certificate", flush=True)
            # Persisted certificate remains usable across restart, without reissuing it.
            docker("restart", edges[0][0])
            edges[0] = (edges[0][0], port(edges[0][0], 443))
            until(lambda: profile(edges[0][1]))
            for _, number in edges:
                status, _, _ = fetch(number, issued_trust, "/metrics")
                assert status == 404, "metrics leaked onto the public listener"
            docker("stop", origin)
            for name, number in edges:
                for _ in range(3):
                    assert profile(number).get("X-Mikan-Offline") == "true"
                metrics = docker("exec", name, "wget", "-Y", "off", "-qO-", "http://127.0.0.1:9091/metrics")
                assert "mikan_sub_circuit_skipped_total 2" in metrics
                assert "ci-subscription-token" not in metrics and TOKEN not in metrics and "ci-device" not in metrics
            docker("start", origin)
            time.sleep(30.1)
            for _, number in edges:
                def online(number=number):
                    headers = profile(number)
                    assert "X-Mikan-Offline" not in headers
                    return headers
                until(online)
            print("PASS: two HTTP-01 issuances, trusted TLS, certificate restart, two-edge outage/recovery, private metrics")
    except BaseException:
        for name in containers:
            subprocess.run(["docker", "logs", "--tail", "50", name], check=False)
        raise
    finally:
        for name in reversed(containers):
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for volume in volumes:
            subprocess.run(["docker", "volume", "rm", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(["docker", "network", "rm", network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
