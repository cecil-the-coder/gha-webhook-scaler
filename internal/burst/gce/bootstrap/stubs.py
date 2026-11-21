#!/usr/bin/env python3
# Loopback stubs for test-runner-bootstrap.sh: a GCE metadata server (HTTP)
# and one TLS server that plays both the scaler's /v1/runner-config and the
# GitHub release API + download. Nothing leaves 127.0.0.1.
#
# Usage: stubs.py <portfile> <config-status> <cert.pem> <key.pem> <tarball> [runner-env-file]
#   config-status: 200 serves a JIT config; any other number is returned as-is.
#   tarball: served as the runner download; its sha256 is the release digest.
import hashlib
import http.server
import json
import os
import ssl
import sys
import threading
import urllib.parse

PORTFILE, STATUS, CERT, KEY, TARBALL = sys.argv[1:6]
RUNNER_ENV_FILE = sys.argv[6] if len(sys.argv) > 6 else ""
TOKEN = "stub.identity-token"
BLOB = "c3R1Yi1qaXQtYmxvYi0xMjM0NTY3ODk="
VERSION = "9.9.9"
with open(TARBALL, "rb") as f:
    TAR = f.read()
TAR_SHA = hashlib.sha256(TAR).hexdigest()
if os.environ.get("STUB_BAD_SUM") == "1":  # advertise a digest the download will not match
    TAR_SHA = hashlib.sha256(b"something else").hexdigest()


class Quiet(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _send(self, code, body, ctype="text/plain"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def config_url():
    return f"https://127.0.0.1:{BPORT}/v1/runner-config"


class Remote(Quiet):
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n:
            self.rfile.read(n)
        if self.path != "/v1/runner-config":
            return self._send(404, b"Not Found")
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            return self._send(401, b"Unauthorized")
        if STATUS == "200":
            return self._send(200, json.dumps({"encoded_jit_config": BLOB}).encode(), "application/json")
        return self._send(int(STATUS), b"stub status")

    def do_GET(self):
        if self.path == "/repos/actions/runner/releases/latest":
            assets = []
            for arch in ("x64", "arm64"):
                name = f"actions-runner-linux-{arch}-{VERSION}.tar.gz"
                assets.append({"name": name, "digest": "sha256:" + TAR_SHA,
                               "browser_download_url": f"https://127.0.0.1:{BPORT}/dl/{name}"})
            return self._send(200, json.dumps({"tag_name": "v" + VERSION, "assets": assets}).encode())
        if self.path.startswith("/dl/"):
            return self._send(200, TAR, "application/gzip")
        return self._send(404, b"Not Found")


class Meta(Quiet):
    def do_GET(self):
        if self.headers.get("Metadata-Flavor") != "Google":
            return self._send(403, b"Forbidden")
        url = urllib.parse.urlsplit(self.path)
        base = "/computeMetadata/v1/"
        if not url.path.startswith(base):
            return self._send(404, b"Not Found")
        rel, q = url.path[len(base):], urllib.parse.parse_qs(url.query)
        attrs = {
            "instance/attributes/runner-config-url": config_url(),
            "instance/attributes/runner-version": "latest",
            "instance/attributes/install-docker": "false",
        }
        if rel in attrs:
            return self._send(200, attrs[rel].encode())
        if rel == "instance/attributes/packages" and os.environ.get("STUB_PACKAGES"):
            return self._send(200, os.environ["STUB_PACKAGES"].encode())
        if rel == "instance/attributes/runner-env":
            if not RUNNER_ENV_FILE:
                return self._send(404, b"Not Found")
            with open(RUNNER_ENV_FILE, "rb") as f:
                return self._send(200, f.read())
        if (rel == "instance/service-accounts/default/identity"
                and q.get("audience") == [config_url()] and q.get("format") == ["full"]):
            return self._send(200, TOKEN.encode())
        return self._send(404, b"Not Found")


ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(CERT, KEY)
bsrv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Remote)
bsrv.socket = ctx.wrap_socket(bsrv.socket, server_side=True)
BPORT = bsrv.server_address[1]
msrv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Meta)
MPORT = msrv.server_address[1]

tmp = PORTFILE + ".tmp"
with open(tmp, "w") as f:
    f.write(f"{MPORT} {BPORT}\n")
os.replace(tmp, PORTFILE)

threading.Thread(target=bsrv.serve_forever, daemon=True).start()
msrv.serve_forever()
