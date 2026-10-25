# mikan-sdk

A typed Python client for the API of the [mikan](https://github.com/Miroshka000/mikan) panel, generated from the panel's own OpenAPI spec with [openapi-python-client](https://github.com/openapi-generators/openapi-python-client). Every operation of the API is a function with typed parameters and typed answers, in a blocking and an asyncio version.

Every stable release of the panel has an SDK of the same number: install the one that matches your panel (Settings → General → Updates shows it), for a panel 0.5.3.0 that is `0.5.3.0`. The client reads answers strictly, so one that does not match its panel can refuse an answer: a value of an enumeration it does not know yet, or a required field the panel does not send.

## Install

```sh
pip install mikan-sdk==<version>
```

It needs Python 3.11 or later.

## Sign in with an API key

Make a key in the panel: Settings → Security → API → New key. It looks like `mk_...` and is shown once. The client needs the key and the API address as the panel's API page shows it, `https://<panel>:<port>/<secret path>/api/v1`, with nothing after `/api/v1`.

```python
from mikan_sdk import AuthenticatedClient
from mikan_sdk.api.users import list_users
from mikan_sdk.models import ListUsersState

client = AuthenticatedClient(
    base_url="https://vpn.example.com:8443/SECRET-PATH/api/v1",
    token="mk_...",
)

with client as client:
    response = list_users.sync_detailed(client=client, state=ListUsersState.ACTIVE, limit=10)
    if response.status_code != 200:
        raise RuntimeError(response.parsed.detail)
    for user in response.parsed.items:
        print(user.id, user.name)
```

Each operation lives in the module of its section: `mikan_sdk.api.users.list_users`, `mikan_sdk.api.tariffs.list_tariffs`, and so on. A module has `sync`, `sync_detailed`, `asyncio` and `asyncio_detailed`; the detailed ones give the status code, the headers and the raw body too. The bodies of requests are in `mikan_sdk.models`.

An error comes as the problem document of RFC 9457 (`ErrorModel`): `detail` holds the code of the error. A key with "Read" access calls only GET, and the operations marked "session only" (sign-in, keys, balance changes) refuse every key with 403 `session_only`. The API page of the docs has the rest: <https://miroshka000.github.io/mikan/api/>.
