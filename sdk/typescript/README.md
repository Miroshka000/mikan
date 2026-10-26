# @getmikan/sdk

A typed TypeScript client for the API of the [mikan](https://github.com/Miroshka000/mikan) panel. The types are generated from the panel's own OpenAPI spec with [openapi-typescript](https://openapi-ts.dev/), the calls go through [openapi-fetch](https://openapi-ts.dev/openapi-fetch/) (the pair the admin panel's own web client uses): a path, its method, and the parameters and bodies the API takes, all checked by the compiler.

Every stable release of the panel has an SDK of the same number: install the one that matches your panel (Settings → System → Updates shows it). npm versions have three numbers and a panel release has four, so the fourth goes after a hyphen: for a panel 0.5.3.0 the SDK is `0.5.3-0`. npm calls such a version a pre-release, so name it exactly and do not use a range.

## Install

```sh
npm install @getmikan/sdk@<version>
```

It needs Node 18 or later, or any runtime with `fetch`.

## Sign in with an API key

Make a key in the panel: Settings → Security → API → New key. It looks like `mk_...` and is shown once. The client needs the key and the API address as the panel's API page shows it, `https://<panel>:<port>/<secret path>/api/v1`, with nothing after `/api/v1`.

```ts
import { createClient } from "@getmikan/sdk";

const mikan = createClient({
  baseUrl: "https://vpn.example.com:8443/SECRET-PATH/api/v1",
  apiKey: process.env.MIKAN_API_KEY!,
});

const { data, error } = await mikan.GET("/users", {
  params: { query: { state: "active", limit: 10 } },
});
if (error) throw new Error(error.detail);
for (const user of data.items) console.log(user.id, user.name);
```

Every call gives `{ data, error, response }`: `data` on success, `error` as the problem document of RFC 9457 (`detail` holds the code of the error) on a refusal, and the `response` itself. The types of the API are exported too: `components["schemas"]["UserView"]`, `paths`, `operations`.

A key with "Read" access calls only GET, and the operations marked "session only" (sign-in, keys, balance changes) refuse every key with 403 `session_only`. The API page of the docs has the rest: <https://miroshka000.github.io/mikan/api/>.
