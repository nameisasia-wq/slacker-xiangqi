"""Play a host/join game with real CLI processes, locally or with an SSH guest."""

import json
import argparse
import ipaddress
import os
import queue
import re
import select
import socket
import socketserver
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MOVES = (
    "7770 1219 0919 8070 7987 7279 8779 7079 8979 6364 1737 4344 "
    "1910 0010 7978 1011 4645 4445 8685 4546 2625 3041 7828 4647 "
    "2838 4737 8584 3738 6665 1112 8483 1219 2907 1939"
).split()


class EdgeProxy(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


class EdgeProxyHandler(socketserver.BaseRequestHandler):
    def handle(self):
        request = b""
        while b"\r\n\r\n" not in request and len(request) < 8192:
            chunk = self.request.recv(4096)
            if not chunk:
                return
            request += chunk
        if not request.startswith(b"CONNECT "):
            return
        upstream = socket.create_connection((self.server.edge_ip, 443), timeout=8)
        with upstream:
            self.request.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            while True:
                ready, _, _ = select.select([self.request, upstream], [], [], 30)
                if not ready:
                    return
                for source in ready:
                    data = source.recv(65536)
                    if not data:
                        return
                    (upstream if source is self.request else self.request).sendall(data)


def public_edge_ip(hostname):
    lookup = subprocess.run(["dig", "+short", "@1.1.1.1", hostname], capture_output=True, text=True)
    for line in lookup.stdout.splitlines():
        try:
            ipaddress.ip_address(line.strip())
            return line.strip()
        except ValueError:
            pass
    return None


class LostResponseProxy(ThreadingHTTPServer):
    daemon_threads = True


class LostResponseHandler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_GET(self):
        self.forward()

    def do_POST(self):
        self.forward()

    def forward(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        request = urllib.request.Request(
            self.server.origin + self.path,
            data=body if self.command == "POST" else None,
            headers={"Authorization": self.headers.get("Authorization", "")},
            method=self.command,
        )
        try:
            with urllib.request.urlopen(request, timeout=5) as response:
                payload = response.read()
                code = response.status
        except urllib.error.HTTPError as error:
            payload, code = error.read(), error.code
        if self.path == "/move" and json.loads(body).get("version") == len(MOVES) - 1:
            self.send_error(502, "injected lost response after host accepted move")
            return
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def state(url, token):
    request = urllib.request.Request(url + "/state", headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(request, timeout=3) as response:
        return json.load(response)


def wait_version(url, token, version):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        current = state(url, token)
        if current["version"] == version:
            return current
        if current["version"] > version:
            raise AssertionError(f"version jumped past {version}: {current['version']}")
        time.sleep(0.05)
    raise TimeoutError(f"version {version} was never reached")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tunnel-binary", help="cloudflared path; guest connects through a Quick Tunnel")
    parser.add_argument("--auto-tunnel", action="store_true", help="host starts and manages the Quick Tunnel itself")
    parser.add_argument("--drop-final-response", action="store_true", help="inject 502 after host accepts the last move")
    parser.add_argument("--quit-early", action="store_true", help="verify guest and host can quit before the game ends")
    parser.add_argument("--host-quit-early", action="store_true", help="verify guest is notified when host quits")
    parser.add_argument("--remote-guest", help="SSH target (user@host) for a guest on another machine")
    parser.add_argument("--remote-binary", help="guest executable path on the SSH target")
    parser.add_argument("--ssh-key", help="private key for the remote guest SSH connection")
    parser.add_argument("--ssh-known-hosts", help="verified known_hosts file for the SSH target")
    parser.add_argument("--host-lang", choices=("en", "zh"), default="en")
    parser.add_argument("--guest-lang", choices=("en", "zh"), default="en")
    args = parser.parse_args()
    if (args.tunnel_binary or args.auto_tunnel) and args.drop_final_response:
        parser.error("use --drop-final-response with the local route")
    if (args.quit_early or args.host_quit_early) and args.drop_final_response:
        parser.error("early-quit checks cannot use --drop-final-response")
    if args.quit_early and args.host_quit_early:
        parser.error("choose one early-quit direction")
    if args.remote_guest and not all((args.remote_binary, args.ssh_key, args.ssh_known_hosts, args.tunnel_binary or args.auto_tunnel)):
        parser.error("remote guest requires --remote-binary, --ssh-key, --ssh-known-hosts and a tunnel mode")
    with tempfile.TemporaryDirectory() as temp:
        binary = str(Path(temp) / "moyu")
        subprocess.run(["go", "build", "-o", binary, "./cmd/moyu"], cwd=ROOT, check=True)
        host_command = [binary, "host", "--listen", "127.0.0.1:0", "--lang", args.host_lang]
        if args.auto_tunnel:
            host_command.append("--tunnel")
            if args.tunnel_binary:
                host_command.extend(("--tunnel-bin", args.tunnel_binary))
        host = subprocess.Popen(
            host_command,
            cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, encoding="utf-8", bufsize=1,
        )
        guest = None
        tunnel = None
        proxy = None
        fault_proxy = None
        logs = {}
        readers = []
        def drain(name, stream, on_line=None):
            chunks = []
            logs[name] = chunks
            def consume():
                for line in iter(stream.readline, ""):
                    chunks.append(line)
                    if on_line:
                        on_line(line)
            thread = threading.Thread(target=consume, daemon=True)
            thread.start()
            readers.append(thread)
        try:
            address = host.stdout.readline()
            invitation = host.stdout.readline()
            match = re.search(r"http://127\.0\.0\.1:\d+", address)
            token_match = re.search(r"([0-9a-f]{48})", invitation)
            if not match or not token_match:
                raise AssertionError(f"host did not provide an address and token: {address!r} {invitation!r}")
            url, token = match.group(), token_match.group(1)
            logs["host stdout"] = [address, invitation]
            host_addresses = queue.Queue()
            def find_host_url(line):
                found = re.search(r"https://[a-z0-9-]+\.trycloudflare\.com", line)
                if found:
                    host_addresses.put(found.group())
            drain("host remainder", host.stdout, find_host_url)
            drain("host stderr", host.stderr)
            guest_url = url
            if args.drop_final_response:
                fault_proxy = LostResponseProxy(("127.0.0.1", 0), LostResponseHandler)
                fault_proxy.origin = url
                threading.Thread(target=fault_proxy.serve_forever, daemon=True).start()
                guest_url = f"http://127.0.0.1:{fault_proxy.server_address[1]}"
            if args.auto_tunnel:
                guest_url = host_addresses.get(timeout=100)
                hostname = guest_url.removeprefix("https://")
                edge_ip = public_edge_ip(hostname)
                if not edge_ip:
                    raise TimeoutError("automatic Quick Tunnel hostname did not resolve publicly")
                proxy = EdgeProxy(("127.0.0.1", 0), EdgeProxyHandler)
                proxy.edge_ip = edge_ip
                threading.Thread(target=proxy.serve_forever, daemon=True).start()
            elif args.tunnel_binary:
                ready = False
                for attempt in range(4):
                    tunnel = subprocess.Popen(
                        [args.tunnel_binary, "tunnel", "--url", url, "--no-autoupdate"],
                        cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                        text=True, encoding="utf-8", bufsize=1,
                    )
                    addresses = queue.Queue()
                    def find_url(line):
                        found = re.search(r"https://[a-z0-9-]+\.trycloudflare\.com", line)
                        if found:
                            addresses.put(found.group())
                    drain("tunnel stdout", tunnel.stdout, find_url)
                    drain("tunnel stderr", tunnel.stderr, find_url)
                    guest_url = addresses.get(timeout=30)
                    # Query public DNS: the local resolver may cache NXDOMAIN for
                    # fresh Quick Tunnel names even after Cloudflare publishes them.
                    hostname = guest_url.removeprefix("https://")
                    deadline = time.monotonic() + 30
                    while time.monotonic() < deadline:
                        edge_ip = public_edge_ip(hostname)
                        if edge_ip:
                            check = subprocess.run(
                                ["curl", "-fsS", "--connect-timeout", "3", "--max-time", "5",
                                 "--resolve", f"{hostname}:443:{edge_ip}", "-o", "/dev/null",
                                 "-H", "Authorization: Bearer " + token, guest_url + "/state"],
                                capture_output=True, text=True,
                            )
                            if check.returncode == 0:
                                ready = True
                                break
                        time.sleep(0.5)
                    if ready:
                        break
                    tunnel.terminate()
                    tunnel.wait(timeout=5)
                if not ready:
                    tail = "".join(logs["tunnel stderr"][-8:])
                    raise TimeoutError(f"Quick Tunnel did not become reachable after 4 URLs\n{tail}")
                proxy = EdgeProxy(("127.0.0.1", 0), EdgeProxyHandler)
                proxy.edge_ip = edge_ip
                threading.Thread(target=proxy.serve_forever, daemon=True).start()
            env = os.environ.copy()
            guest_command = [binary, "join", "--url", guest_url, "--lang", args.guest_lang]
            if args.remote_guest:
                guest_command = [
                    "ssh", "-T", "-i", args.ssh_key,
                    "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
                    "-o", "StrictHostKeyChecking=yes",
                    "-o", f"UserKnownHostsFile={args.ssh_known_hosts}",
                    args.remote_guest, args.remote_binary, "join", "--url", guest_url, "--lang", args.guest_lang,
                ]
            else:
                env["MOYU_JOIN_TOKEN"] = token
            if proxy and not args.remote_guest:
                env["HTTPS_PROXY"] = f"http://127.0.0.1:{proxy.server_address[1]}"
            guest = subprocess.Popen(
                guest_command, cwd=ROOT, env=env,
                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                text=True, encoding="utf-8", bufsize=1,
            )
            drain("guest stdout", guest.stdout)
            drain("guest stderr", guest.stderr)
            if args.remote_guest:
                guest.stdin.write(token + "\n")
                guest.stdin.flush()
            if args.quit_early:
                host.stdin.write(MOVES[0] + "\n")
                host.stdin.flush()
                wait_version(url, token, 1)
                guest.stdin.write("q\n")
                guest.stdin.flush()
                guest.wait(timeout=15)
                host.wait(timeout=15)
                if guest.returncode != 0 or host.returncode != 0:
                    raise AssertionError(f"quit failed: host={host.returncode}, guest={guest.returncode}")
                if "客方已退出" not in "".join(logs["host remainder"]):
                    raise AssertionError("host did not report that the guest left")
                print("PASS: guest quit ended both clients before the game ended")
                return
            if args.host_quit_early:
                deadline = time.monotonic() + 8
                while time.monotonic() < deadline and not state(url, token).get("guestJoined"):
                    time.sleep(0.1)
                if not state(url, token).get("guestJoined"):
                    raise AssertionError("guest never joined")
                host.stdin.write("q\n")
                host.stdin.flush()
                host.wait(timeout=15)
                guest.wait(timeout=15)
                if host.returncode != 0 or guest.returncode != 0:
                    raise AssertionError(f"quit failed: host={host.returncode}, guest={guest.returncode}")
                if "房主已退出" not in "".join(logs["guest stdout"]):
                    raise AssertionError("guest did not report that the host left")
                print("PASS: host quit notified and ended the guest client")
                return
            for i, move in enumerate(MOVES):
                before = wait_version(url, token, i)
                expected_turn = 1 if i % 2 == 0 else 2
                if before["turn"] != expected_turn:
                    raise AssertionError(f"turn {i}: got side {before['turn']}, expected {expected_turn}")
                player = host if i % 2 == 0 else guest
                player.stdin.write(move + "\n")
                player.stdin.flush()
                after = wait_version(url, token, i + 1)
                if after["history"][-1] != move:
                    raise AssertionError(f"move {i} was not synchronized: {move}")
            if after["winner"] != 2 or len(after["history"]) != len(MOVES):
                raise AssertionError("the game did not finish with the expected black win")
            for name, player in (("host", host), ("guest", guest)):
                player.wait(timeout=8)
                out = "".join(logs[f"{name} remainder"] if name == "host" else logs["guest stdout"])
                err = "".join(logs[f"{name} stderr"])
                if player.returncode != 0 or "黑方获胜" not in out:
                    raise AssertionError(f"{name} did not report the result: exit={player.returncode} stderr={err}")
            for reader in readers:
                reader.join(timeout=1)
            host_output = "".join(logs["host remainder"])
            guest_output = "".join(logs["guest stdout"])
            if ("车" if args.host_lang == "zh" else "R1") not in host_output:
                raise AssertionError("host did not render its selected language")
            if ("卒" if args.guest_lang == "zh" else "r1") not in guest_output:
                raise AssertionError("guest did not render its selected language")
            if "     8   7   6" not in guest_output or " 9   " not in guest_output:
                raise AssertionError("guest board was not rotated")
            route = "automatic Quick Tunnel with remote guest" if args.remote_guest and args.auto_tunnel else "Quick Tunnel with remote guest" if args.remote_guest else "automatic Quick Tunnel" if args.auto_tunnel else "Quick Tunnel" if tunnel else "localhost with lost final response" if fault_proxy else "localhost"
            print(f"PASS: host/join through {route} completed {len(MOVES)} plies; both terminals reported a black win")
        finally:
            for player in (guest, tunnel, host):
                if player is not None and player.poll() is None:
                    player.terminate()
                    try:
                        player.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        player.kill()
                        player.wait()
            if proxy:
                proxy.shutdown()
                proxy.server_close()
            if fault_proxy:
                fault_proxy.shutdown()
                fault_proxy.server_close()


if __name__ == "__main__":
    main()
