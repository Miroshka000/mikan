"""An isolated TLS origin for the relay integration test; no real panel data."""
import http.server
import ssl


class Panel(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer ci-subscription-token":
            self.send_error(401)
            return
        if self.headers.get("Cookie") or not self.path.startswith("/api/subscription/"):
            self.send_error(400)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/yaml")
        self.send_header("Subscription-Userinfo", "upload=0; download=0; total=1000000; expire=0")
        self.end_headers()
        self.wfile.write(b"proxies: []\n")


server = http.server.ThreadingHTTPServer(("0.0.0.0", 8443), Panel)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain("/tmp/test-ca.pem", "/tmp/test-key.pem")
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
