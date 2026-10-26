#!/usr/bin/env python3
"""Test real proxy handshakes against an independent loopback sing-box server.

Requires Go, OpenSSL and a separately installed sing-box (tested with 1.14.3).
Run prepare-wing.py first. No provider credentials or router access are used.
"""
import argparse
import base64
import contextlib
import http.server
import json
import os
from pathlib import Path
import socket
import socketserver
import ssl
import struct
import subprocess
import tempfile
import threading
import time
import urllib.parse


class HTTP(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def do_HEAD(self):
        self.send_response(204)
        self.send_header('Content-Length', '0')
        self.end_headers()

    def do_GET(self):
        data = b"protocol-payload" * 4096 if self.path in ('/payload', '/stream') else b""
        self.send_response(200 if data else 204)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        for offset in range(0, len(data), 8192):
            self.wfile.write(data[offset:offset+8192])
            if self.path == '/stream':
                self.wfile.flush()
                time.sleep(0.03)

    def log_message(self, *args):
        pass


class UDP(socketserver.BaseRequestHandler):
    def handle(self):
        data, sock = self.request
        if len(data) > 16 and struct.unpack('!HH', data[2:6]) == (0x100, 1):
            end = data.find(b'\x00', 12) + 5
            if end > 16 and end <= len(data):
                answer = b'\xc0\x0c' + struct.pack('!HHIH', 1, 1, 60, 4) + socket.inet_aton('192.0.2.1')
                sock.sendto(data[:2] + struct.pack('!HHHHH', 0x8180, 1, 1, 0, 0) + data[12:end] + answer, self.client_address)
                return
        sock.sendto(data, self.client_address)


def port(udp=False):
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM if udp else socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def run(args):
    with http.server.ThreadingHTTPServer(("127.0.0.1", 0), HTTP) as http_server, socketserver.UDPServer(("127.0.0.1", 0), UDP) as udp:
        for server in (http_server, udp):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            return run_fixture(args, http_server.server_port, udp.server_address[1])
        finally:
            http_server.shutdown()
            udp.shutdown()


def run_fixture(args, http_port, udp_port):
    with tempfile.TemporaryDirectory(prefix="daed-protocol-") as temp, contextlib.ExitStack() as cleanup:
        work = Path(temp)
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(work / "key.pem"),
            "-out", str(work / "cert.pem"), "-subj", "/CN=fixture.test", "-addext", "subjectAltName=DNS:fixture.test,IP:127.0.0.1", "-days", "1",
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        tls = {"enabled": True, "certificate_path": str(work / "cert.pem"), "key_path": str(work / "key.pem")}
        # Trust only a separate local VLESS test CA. Other protocol fixtures keep
        # their untrusted certificate so negative TLS checks remain meaningful.
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(work / "vless-key.pem"),
            "-out", str(work / "vless-cert.pem"), "-subj", "/CN=vless.fixture.test", "-addext", "subjectAltName=DNS:vless.fixture.test", "-days", "1",
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        vless_tls = {"enabled": True, "certificate_path": str(work / "vless-cert.pem"), "key_path": str(work / "vless-key.pem")}
        cover = http.server.ThreadingHTTPServer(('127.0.0.1', 0), HTTP)
        cover_tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        cover_tls.minimum_version = ssl.TLSVersion.TLSv1_3
        cover_tls.load_cert_chain(work / 'vless-cert.pem', work / 'vless-key.pem')
        cover_tls.set_alpn_protocols(['h2', 'http/1.1'])
        cover.socket = cover_tls.wrap_socket(cover.socket, server_side=True)
        threading.Thread(target=cover.serve_forever, daemon=True).start()
        cleanup.callback(cover.server_close)
        cleanup.callback(cover.shutdown)
        pair = subprocess.check_output([str(args.sing_box), 'generate', 'reality-keypair'], text=True)
        reality_keys = dict(line.split(': ', 1) for line in pair.strip().splitlines())
        inbounds, cases, native_cases = [], [], []
        def native_case(name, value, family, tcp_only=False):
            encoded = base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip('=')
            native_cases.append({'Name': name, 'Link': 'singbox://' + encoded, 'Family': family, 'TCPOnly': tcp_only})
        h1 = port(True)
        inbounds.append({'type': 'hysteria', 'tag': 'hysteria1', 'listen': '127.0.0.1', 'listen_port': h1,
                         'up_mbps': 20, 'down_mbps': 100, 'users': [{'auth_str': 'fixture'}], 'tls': vless_tls})
        native_cases.append({'Name': 'hysteria1-uri', 'Link': f'hysteria://fixture@127.0.0.1:{h1}?peer=vless.fixture.test&upmbps=20&downmbps=100'})
        snell = port()
        inbounds.append({'type': 'snell', 'tag': 'snell6', 'version': 6, 'listen': '127.0.0.1', 'listen_port': snell, 'psk': 'fixture-snell-password'})
        native_cases.append({'Name': 'snell6-uri', 'Link': f'snell://fixture-snell-password@127.0.0.1:{snell}?version=6'})
        inner, outer = port(), port()
        inbounds.extend([
            {'type': 'shadowsocks', 'tag': 'shadow-inner', 'listen': '127.0.0.1', 'listen_port': inner, 'method': 'aes-128-gcm', 'password': 'fixture'},
            {'type': 'shadowtls', 'tag': 'shadow-outer', 'listen': '127.0.0.1', 'listen_port': outer, 'version': 3,
             'users': [{'password': 'fixture'}], 'handshake': {'server': '127.0.0.1', 'server_port': cover.server_port}, 'detour': 'shadow-inner'},
        ])
        native_case('shadowtls3-bundle', {'tag': 'inner', 'outbounds': [
            {'type': 'shadowsocks', 'tag': 'inner', 'server': '127.0.0.1', 'server_port': inner, 'method': 'aes-128-gcm', 'password': 'fixture', 'detour': 'outer'},
            {'type': 'shadowtls', 'tag': 'outer', 'server': '127.0.0.1', 'server_port': outer, 'version': 3, 'password': 'fixture', 'tls': {'enabled': True, 'server_name': 'vless.fixture.test'}},
        ]}, 'shadowtls', True)
        ssh_port = port()
        subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(work / 'ssh-key')], check=True)
        ssh_source = Path(__file__).with_name('ssh_fixture.go')
        subprocess.run(['go', 'build', '-o', str(work / 'ssh-fixture'), str(ssh_source)], cwd=args.wing, check=True)
        ssh_process = subprocess.Popen([str(work / 'ssh-fixture'), str(work / 'ssh-key'), str(ssh_port), str(http_port)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        cleanup.callback(ssh_process.wait, 5)
        cleanup.callback(ssh_process.terminate)
        host_key = ' '.join((work / 'ssh-key.pub').read_text().split()[:2])
        native_cases.append({'Name': 'ssh-uri-host-key', 'Link': f'ssh://fixture:fixture@127.0.0.1:{ssh_port}?host_key=' + urllib.parse.quote(host_key, safe=''), 'TCPOnly': True})
        native_case('ssh-native-json', {'type': 'ssh', 'server': '127.0.0.1', 'server_port': ssh_port, 'user': 'fixture', 'password': 'fixture', 'host_key': [host_key]}, 'ssh-json', True)
        reality_port = port()
        reality_uuid = '00000000-0000-4000-8000-000000000002'
        inbounds.append({'type': 'vless', 'tag': 'reality-vision', 'listen': '127.0.0.1', 'listen_port': reality_port,
                         'users': [{'uuid': reality_uuid, 'flow': 'xtls-rprx-vision'}],
                         'tls': {'enabled': True, 'server_name': 'vless.fixture.test', 'reality': {'enabled': True,
                             'handshake': {'server': '127.0.0.1', 'server_port': cover.server_port},
                             'private_key': reality_keys['PrivateKey'], 'short_id': ['abcd']}}})
        reality_link = (f'vless://{reality_uuid}@127.0.0.1:{reality_port}?security=reality&sni=vless.fixture.test'
                        f'&pbk={reality_keys["PublicKey"]}&sid=abcd&fp=chrome&flow=xtls-rprx-vision&headerType=none&spx=%2F')
        cases.append({'Name': 'vless-reality-vision', 'Link': reality_link})
        native_cases.append({'Name': 'reality-exporter-hybrid-hint', 'Link': reality_link + '&support-x25519mlkem768=true'})
        cases.append({'Name': 'vless-reality-reject-wrong-short-id', 'Link': reality_link.replace('sid=abcd', 'sid=ffff'), 'Failure': 'unreachable'})
        for method in ["aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305"]:
            number = port()
            password = "fixture@pass:2026"
            if method.startswith("2022"):
                password = base64.b64encode(b"k" * (16 if method == "2022-blake3-aes-128-gcm" else 32)).decode()
            inbounds.append({"type": "shadowsocks", "tag": method, "listen": "127.0.0.1", "listen_port": number, "method": method, "password": password})
            user = base64.urlsafe_b64encode((method + ":" + password).encode()).decode().rstrip("=")
            cases.append({"Name": method, "Link": f"ss://{user}@127.0.0.1:{number}"})
            if method == 'chacha20-ietf-poly1305':
                native_cases.append({'Name': 'ss-exporter-plain-tcp', 'Link': f'ss://{user}@127.0.0.1:{number}?type=tcp&headerType=none'})
            if method.startswith('2022'):
                cases.append({"Name": method + '-sip002', "Link": f"ss://{method}:{urllib.parse.quote(password, safe='')}@127.0.0.1:{number}"})
        for kind in ['vmess', 'trojan']:
            for transport in ['tcp', 'ws']:
                number = port()
                uuid = '00000000-0000-4000-8000-000000000001'
                inbound = {'type': kind, 'tag': kind + '-' + transport, 'listen': '127.0.0.1', 'listen_port': number, 'tls': vless_tls}
                inbound['users'] = [{'uuid': uuid, 'alterId': 0}] if kind == 'vmess' else [{'password': 'fixture@pass:2026'}]
                if transport == 'ws':
                    inbound['transport'] = {'type': 'ws', 'path': '/fixture'}
                inbounds.append(inbound)
                if kind == 'vmess':
                    config = {'v': '2', 'ps': kind + '-' + transport, 'add': '127.0.0.1', 'port': str(number), 'id': uuid, 'aid': '0', 'scy': 'auto', 'net': transport, 'type': 'none', 'host': '', 'path': '/fixture' if transport == 'ws' else '', 'tls': 'tls', 'sni': 'vless.fixture.test'}
                    link = 'vmess://' + base64.b64encode(json.dumps(config).encode()).decode()
                    if transport == 'ws':
                        exported = {**config, 'class': 3, 'remark': 'export label', 'headerType': 'none', 'verify_cert': True}
                        native_cases.append({'Name': 'vmess-exporter-certificate-verified', 'Link': 'vmess://' + base64.b64encode(json.dumps(exported).encode()).decode()})
                        exported['sni'] = 'mismatch.invalid'
                        native_cases.append({'Name': 'vmess-exporter-certificate-rejected', 'Link': 'vmess://' + base64.b64encode(json.dumps(exported).encode()).decode(), 'Failure': 'tls_failed'})
                        exported['verify_cert'] = False
                        native_cases.append({'Name': 'vmess-exporter-explicit-insecure', 'Link': 'vmess://' + base64.b64encode(json.dumps(exported).encode()).decode()})
                else:
                    link = f"trojan://fixture%40pass%3A2026@127.0.0.1:{number}?sni=vless.fixture.test&type={transport}"
                    if transport == 'ws':
                        link += '&path=%2Ffixture'
                cases.append({'Name': kind + '-tls-' + transport, 'Link': link})
                if kind == 'trojan' and transport == 'tcp':
                    cases.append({'Name': 'trojan-exporter-udp-enabled', 'Link': link + '&udp=1'})
        number = port(True)
        inbounds.append({'type': 'tuic', 'tag': 'tuic', 'listen': '127.0.0.1', 'listen_port': number,
                         'users': [{'uuid': uuid, 'password': 'fixture@pass:2026'}], 'congestion_control': 'cubic',
                         'tls': {**vless_tls, 'alpn': ['h3']}})
        cases.append({'Name': 'tuic-v5', 'Link': f'tuic://{uuid}:fixture%40pass%3A2026@127.0.0.1:{number}?sni=vless.fixture.test&alpn=h3&congestion_control=cubic&udp_relay_mode=native'})
        # Cover the user's VLESS path with independent TLS and WebSocket servers.
        for transport in ["tcp", "ws", "grpc"]:
            number = port()
            uuid = "00000000-0000-4000-8000-000000000001"
            inbound = {"type": "vless", "tag": "vless-" + transport, "listen": "127.0.0.1", "listen_port": number,
                       "users": [{"name": "fixture", "uuid": uuid}], "tls": vless_tls}
            if transport == "ws":
                inbound["transport"] = {"type": "ws", "path": "/fixture"}
            elif transport == "grpc":
                inbound["transport"] = {"type": "grpc", "service_name": "fixture"}
            inbounds.append(inbound)
            link = f"vless://{uuid}@127.0.0.1:{number}?security=tls&sni=vless.fixture.test&type={transport}"
            if transport == 'tcp':
                link += '&headerType=none'
            if transport == 'ws':
                link += '&path=%2Ffixture'
            elif transport == 'grpc':
                link += '&serviceName=fixture'
            cases.append({"Name": "vless-tls-" + transport, "Link": link})
            cases.append({"Name": "vless-tls-" + transport + "-certificate-rejected", "Link": link.replace("sni=vless.fixture.test", "sni=mismatch.invalid"), "Failure": "tls_failed"})
        for kind in ["hysteria2", "anytls"]:
            number = port(kind == "hysteria2")
            password = "fixture:pass:2026" if kind == "hysteria2" else "fixture@pass:2026"
            inbounds.append({"type": kind, "tag": kind, "listen": "127.0.0.1", "listen_port": number, "users": [{"name": "fixture", "password": password}], "tls": tls})
            link = f"{kind}://{urllib.parse.quote(password, safe='')}@127.0.0.1:{number}?sni=fixture.test&insecure=1"
            cases.append({"Name": kind, "Link": link})
            if kind == "anytls":
                for option in ["insecure=true", "allowInsecure=true"]:
                    cases.append({"Name": f"anytls-{option}", "Link": link.replace("insecure=1", option)})
                cases.append({"Name": "anytls-certificate-rejected", "Link": link.replace("insecure=1", "insecure=0"), "Failure": "tls_failed"})
            else:
                cases.append({"Name": "hy2-user-password", "Link": link.replace(urllib.parse.quote(password, safe=""), "fixture:pass%3A2026")})
        number = port(True)
        inbounds.append({"type": "hysteria2", "tag": "hy2-obfs", "listen": "127.0.0.1", "listen_port": number, "users": [{"name": "fixture", "password": "fixture"}], "tls": tls, "obfs": {"type": "salamander", "password": "fixture-obfs"}})
        link = f"hy2://fixture@127.0.0.1:{number}?sni=fixture.test&insecure=1&obfs=salamander&obfs-password=fixture-obfs"
        cases.append({"Name": "hysteria2-salamander", "Link": link})
        native_cases.append({'Name': 'hy2-exporter-tls-marker', 'Link': link + '&security=tls'})
        cases.append({"Name": "hysteria2-obfs-alias-ports", "Link": link.replace("obfs-password", "obfsPassword").replace(f":{number}?", ":1?") + f"&ports={number}-{number}"})
        cases.append({"Name": "hysteria2-mport", "Link": link.replace(f":{number}?", ":1?") + f"&mport={number}"})
        # Exercise the exported base64 ECH list against a real independently
        # generated ECH server; merely accepting the config is insufficient.
        ech_pair = subprocess.check_output([str(args.sing_box), 'generate', 'ech-keypair', 'vless.fixture.test'], text=True)
        def pem_block(kind):
            return ech_pair.split('-----BEGIN ' + kind + '-----', 1)[1].split('-----END ' + kind + '-----', 1)[0]
        ech_encoded = ''.join(pem_block('ECH CONFIGS').split())
        ech_key = '-----BEGIN ECH KEYS-----' + pem_block('ECH KEYS') + '-----END ECH KEYS-----\n'
        number = port(True)
        inbounds.append({'type':'hysteria2','tag':'hy2-ech','listen':'127.0.0.1','listen_port':number,
                         'users':[{'password':'ech-fixture'}], 'tls':{**vless_tls,'ech':{'enabled':True,'key':[ech_key]}}})
        native_cases.append({'Name':'hy2-exporter-ech','Link':f'hy2://ech-fixture@127.0.0.1:{number}?security=tls&sni=vless.fixture.test&ech='+urllib.parse.quote(ech_encoded,safe='')})
        juicity_config = None
        if args.juicity_server:
            number = port(True)
            uuid = '00000000-0000-4000-8000-000000000001'
            juicity_config = work / 'juicity.json'
            juicity_config.write_text(json.dumps({'listen': f'127.0.0.1:{number}', 'users': {uuid: 'fixture@pass:2026'},
                'certificate': str(work / 'vless-cert.pem'), 'private_key': str(work / 'vless-key.pem'),
                'congestion_control': 'cubic', 'log_level': 'error'}), encoding='utf-8')
            cases.append({'Name': 'juicity', 'Link': f'juicity://{uuid}:fixture%40pass%3A2026@127.0.0.1:{number}?sni=vless.fixture.test&congestion_control=cubic'})
        config = work / "config.json"
        config.write_text(json.dumps({"log": {"level": "error"}, "inbounds": inbounds, "outbounds": [{"type": "direct", "tag": "direct"}]}), encoding="utf-8")
        casefile = work / "cases.json"
        casefile.write_text(json.dumps({"Target": f"http://127.0.0.1:{http_port}", "UDP": f"127.0.0.1:{udp_port}", "Cases": cases, "NativeCases":native_cases}), encoding="utf-8")
        subprocess.run([str(args.sing_box), "check", "-c", str(config)], check=True)
        with (work / "server.log").open("w", encoding="utf-8") as log:
            process = subprocess.Popen([str(args.sing_box), "run", "-c", str(config)], stdout=log, stderr=subprocess.STDOUT)
            juicity = None
            try:
                if juicity_config:
                    juicity = subprocess.Popen([str(args.juicity_server), 'run', '-c', str(juicity_config)], stdout=log, stderr=subprocess.STDOUT)
                time.sleep(0.6)
                if process.poll() is not None or (juicity and juicity.poll() is not None):
                    raise RuntimeError("independent server exited: " + (work / "server.log").read_text())
                env = os.environ.copy()
                env["DAED_PROTOCOL_CASES"] = str(casefile)
                env["SSL_CERT_FILE"] = str(work / "vless-cert.pem")
                if args.client_singbox:
                    env['DAED_TEST_SINGBOX'] = str(args.client_singbox.resolve(strict=True))
                return subprocess.run(["go", "test", "-count=1", "-timeout=180s", "-v", "./graphql/service/node", "./dae", "github.com/daeuniverse/dae/control", "-run", "TestRouterProtocolInteroperability$|TestRouterCoreProtocolInteroperability$|TestRouterCoreBulkProbe$|TestRouterHTTPProtocolDownload$|TestRouterConnectionProtocolFailover$"], cwd=args.wing, env=env).returncode
            finally:
                process.terminate()
                process.wait(timeout=5)
                if juicity:
                    juicity.terminate()
                    juicity.wait(timeout=5)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wing", type=Path, required=True)
    parser.add_argument("--sing-box", type=Path, required=True)
    parser.add_argument("--juicity-server", type=Path, help="Optional independent official Juicity server (tested with v0.5.0)")
    parser.add_argument('--client-singbox', type=Path, help='Also exercise the optional native sing-box adapter')
    raise SystemExit(run(parser.parse_args()))
