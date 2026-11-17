"""An isolated TLS origin for the relay integration test; no real panel data."""
import http.server
import ssl
from pathlib import Path


class Panel(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def authorized(self):
        return self.headers.get("Authorization") == "Bearer ci-subscription-token" and not Path("/tmp/revoked").exists()

    def do_HEAD(self):
        if self.path != "/api/subscription/_relay/auth":
            self.send_error(404)
            return
        nonce = self.headers.get("X-Mikan-Relay-Nonce", "")
        if not self.authorized():
            if self.headers.get("Authorization") == "Bearer ci-subscription-token" and self.headers.get("X-Mikan-Relay-Proof") == "ci-issued-proof":
                self.send_response(204)
                self.send_header("X-Mikan-Relay-Authenticated", "revoked:" + nonce)
                self.end_headers()
            else:
                self.send_error(404)
            return
        self.send_response(204)
        self.send_header("X-Mikan-Relay-Authenticated", nonce)
        self.end_headers()

    def do_GET(self):
        if not self.authorized():
            self.send_error(404)
            return
        if self.headers.get("Cookie") or not self.path.startswith("/api/subscription/"):
            self.send_error(400)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/yaml")
        self.send_header("X-Mikan-Relay-Authenticated", "1")
        self.send_header("X-Mikan-Relay-Proof", "ci-issued-proof")
        self.send_header("Subscription-Userinfo", "upload=0; download=0; total=1000000; expire=0")
        self.end_headers()
        self.wfile.write(b"proxies: []\n")


server = http.server.ThreadingHTTPServer(("0.0.0.0", 8443), Panel)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain("/tmp/test-ca.pem", "/tmp/test-key.pem")
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
