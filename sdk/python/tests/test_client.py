"""The installed package against a panel stand-in: what goes on the wire (address, key, body)
and what comes back (a parsed answer, a problem+json error). Every operation of the spec is
also there as a function: a generator that quietly dropped some would fail the count."""

import http.server
import json
import os
import threading
import unittest
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

import mikan_sdk
from mikan_sdk import AuthenticatedClient
from mikan_sdk.api.payments import adjust_balance
from mikan_sdk.api.users import extend_user, list_users
from mikan_sdk.models import AdjustBalanceInputBody, ErrorModel, ExtendInputBody, ListUsersState

KEY = "mk_test_key"
EMPTY_LIST = {
    "items": [],
    "total": 0,
    "counts": {"all": 0, "active": 0, "expiring": 0, "limited": 0, "expired": 0, "disabled": 0},
    "folder_counts": {},
    "source_counts": {},
    "users_total": 0,
    "hidden_total": 0,
}


class Panel(http.server.BaseHTTPRequestHandler):
    # What the next request is answered with, and what the last one was.
    reply: tuple[int, str, dict] = (200, "application/json", {})
    seen: dict = {}

    def do_GET(self):
        self.answer()

    def do_POST(self):
        self.answer()

    def answer(self):
        size = int(self.headers.get("Content-Length") or 0)
        Panel.seen = {
            "method": self.command,
            "url": urlsplit(self.path),
            "authorization": self.headers.get("Authorization"),
            "content_type": self.headers.get("Content-Type"),
            "body": self.rfile.read(size),
        }
        status, content_type, body = Panel.reply
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


class ClientTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Panel)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        # The API address as the panel shows it: the secret path, then /api/v1.
        cls.client = AuthenticatedClient(
            base_url=f"http://127.0.0.1:{cls.server.server_port}/secret-path/api/v1", token=KEY
        )

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def test_key_goes_as_a_bearer_token_to_the_api_address(self):
        Panel.reply = (200, "application/json", EMPTY_LIST)
        response = list_users.sync_detailed(client=self.client, state=ListUsersState.ACTIVE, limit=10)
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.parsed.total, 0)
        self.assertEqual(Panel.seen["method"], "GET")
        self.assertEqual(Panel.seen["url"].path, "/secret-path/api/v1/users")
        query = parse_qs(Panel.seen["url"].query)
        self.assertEqual(query["state"], ["active"])
        self.assertEqual(query["limit"], ["10"])
        self.assertEqual(Panel.seen["authorization"], f"Bearer {KEY}")

    def test_json_body_and_path_parameters(self):
        # What comes back does not matter here, only what went out: an error is the short answer.
        Panel.reply = (404, "application/problem+json", {"status": 404, "detail": "not_found"})
        extend_user.sync_detailed(42, client=self.client, body=ExtendInputBody(days=30))
        self.assertEqual(Panel.seen["method"], "POST")
        self.assertEqual(Panel.seen["url"].path, "/secret-path/api/v1/users/42/extend")
        self.assertEqual(Panel.seen["content_type"], "application/json")
        self.assertEqual(json.loads(Panel.seen["body"]), {"days": 30})
        self.assertEqual(Panel.seen["authorization"], f"Bearer {KEY}")

    def test_a_refusal_is_the_problem_document(self):
        Panel.reply = (403, "application/problem+json", {"status": 403, "title": "Forbidden", "detail": "session_only"})
        response = adjust_balance.sync_detailed(
            123, client=self.client, body=AdjustBalanceInputBody(delta=-5000, note="refund for the downtime")
        )
        self.assertEqual(response.status_code, 403)
        self.assertIsInstance(response.parsed, ErrorModel)
        self.assertEqual(response.parsed.detail, "session_only")

    def test_every_operation_of_the_spec_is_a_function(self):
        spec = json.loads(Path(os.environ["MIKAN_SDK_SPEC"]).read_text(encoding="utf-8"))
        operations = sum(
            1 for item in spec["paths"].values() for method in item if method in {"get", "put", "post", "delete", "patch"}
        )
        modules = [
            path
            for path in (Path(mikan_sdk.__file__).parent / "api").glob("*/*.py")
            if path.name != "__init__.py"
        ]
        self.assertEqual(len(modules), operations)


if __name__ == "__main__":
    unittest.main()
