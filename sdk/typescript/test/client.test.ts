// The built package against a panel stand-in: what goes on the wire (address, key, body)
// and what comes back (data, a problem+json error). The calls are also what the types
// check: tsc compiles this file before it runs (pnpm test).
import assert from "node:assert/strict";
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, beforeEach, describe, it } from "node:test";
import { createClient } from "../dist/index.js";

interface Seen {
  method?: string;
  url?: string;
  authorization?: string;
  contentType?: string;
  body: string;
}

let server: Server;
let baseUrl: string;
let seen: Seen;
let reply: { status: number; type: string; body: unknown };

async function read(req: IncomingMessage): Promise<string> {
  let body = "";
  for await (const chunk of req) body += chunk;
  return body;
}

before(async () => {
  server = createServer(async (req, res) => {
    seen = {
      method: req.method,
      url: req.url,
      authorization: req.headers.authorization,
      contentType: req.headers["content-type"],
      body: await read(req),
    };
    res.writeHead(reply.status, { "content-type": reply.type });
    res.end(JSON.stringify(reply.body));
  });
  await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
  // The API address as the panel shows it: the secret path, then /api/v1.
  baseUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}/secret-path/api/v1`;
});

after(() => {
  server.close();
});

beforeEach(() => {
  reply = { status: 200, type: "application/json", body: {} };
});

describe("createClient", () => {
  it("sends the API key as a bearer token to the API address", async () => {
    reply.body = { items: [], total: 0 };
    const client = createClient({ baseUrl, apiKey: "mk_test_key" });
    const { data, error, response } = await client.GET("/users", { params: { query: { state: "active", limit: 10 } } });
    // As the README does it: once the error is ruled out, data is there.
    if (error) throw new Error(error.detail);
    assert.equal(response.status, 200);
    assert.deepEqual(data.items, []);
    assert.equal(seen.method, "GET");
    assert.equal(seen.url, "/secret-path/api/v1/users?state=active&limit=10");
    assert.equal(seen.authorization, "Bearer mk_test_key");
  });

  it("sends a JSON body and fills the path parameters", async () => {
    reply = { status: 200, type: "application/json", body: { id: 42 } };
    const client = createClient({ baseUrl, apiKey: "mk_test_key" });
    await client.POST("/users/{id}/extend", { params: { path: { id: 42 } }, body: { days: 30 } });
    assert.equal(seen.method, "POST");
    assert.equal(seen.url, "/secret-path/api/v1/users/42/extend");
    assert.equal(seen.contentType, "application/json");
    assert.deepEqual(JSON.parse(seen.body), { days: 30 });
    assert.equal(seen.authorization, "Bearer mk_test_key");
  });

  it("returns the problem document of a refusal as the error", async () => {
    reply = { status: 403, type: "application/problem+json", body: { status: 403, title: "Forbidden", detail: "session_only" } };
    const client = createClient({ baseUrl, apiKey: "mk_test_key" });
    const { data, error, response } = await client.POST("/balances/{tg_id}/adjust", {
      params: { path: { tg_id: 123 } },
      body: { delta: -5000, note: "refund for the downtime" },
    });
    assert.equal(data, undefined);
    assert.equal(response.status, 403);
    assert.equal(error?.detail, "session_only");
  });

  it("types the query: a state the API does not know does not compile", () => {
    const client = createClient({ baseUrl, apiKey: "mk_test_key" });
    // Never called: the check is that tsc refuses it (pnpm test compiles this file first).
    const call = () =>
      // @ts-expect-error "nonsense" is not one of the states of the list
      client.GET("/users", { params: { query: { state: "nonsense" } } });
    assert.equal(typeof call, "function");
  });

  it("needs an address and a key", () => {
    assert.throws(() => createClient({ baseUrl: "", apiKey: "mk_test_key" }), /baseUrl/);
    assert.throws(() => createClient({ baseUrl, apiKey: "" }), /apiKey/);
  });
});
