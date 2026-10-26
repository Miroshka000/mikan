# SDKs

Clients for the panel's API, generated from its OpenAPI spec and published as `mikan-sdk`:

| Language | Registry | Generator | Folder |
|---|---|---|---|
| TypeScript | [npm](https://www.npmjs.com/package/mikan-sdk) | [openapi-typescript](https://openapi-ts.dev/) types, [openapi-fetch](https://openapi-ts.dev/openapi-fetch/) calls | [typescript](typescript/) |
| Python | [PyPI](https://pypi.org/project/mikan-sdk/) | [openapi-python-client](https://github.com/openapi-generators/openapi-python-client) | [python](python/) |

The Go SDK comes later. How to use them: <https://miroshka000.github.io/mikan/api/sdk/>.

## Why these generators

- **TypeScript.** The admin panel's own web client is made from the same spec with the same two tools (`web/src/api`, `pnpm api`), so the SDK's types are the ones the admin UI is checked against, with no second toolchain to learn. openapi-typescript and openapi-fetch are maintained by one team, and openapi-fetch is a thin typed layer over `fetch`: no runtime of its own to keep up with the API.
- **Python.** openapi-python-client is a maintained generator that produces a plain, typed package: attrs models, an httpx client, a blocking and an asyncio function for every operation, and nothing to install but those two libraries. It is a Python tool, so the build needs no other runtime; OpenAPI Generator, the common alternative, runs on Java.

## How they are made

- `web/src/api/openapi.json` is what the panel exports (`go run ./cmd/mikan openapi`). CI fails a pull request when the file differs from the code, so a change of the API reaches the SDKs in the pull request that makes it.
- `spec.mjs` writes the spec the generators read: the same operations and schemas, without `/api/v1` in the paths (the SDKs take the API address as the panel's API page shows it, with `/api/v1` at its end) and with the release's number as the version.
- `sh sdk/build.sh typescript|python [version]` generates the package, tests it against a stand-in of the panel and packs it into `sdk/dist`. Without a version it is a check build (CI runs it on every pull request); the generated code is never kept in git. TypeScript needs node and pnpm, Python needs Python 3.11 or later and node.
- The Python build runs from `python/requirements.txt`, which pins the generator and its dependencies with hashes. After a change of `python/requirements.in`, regenerate it with the command written there.

## Versions

An SDK has the number of the panel release it is built from. PyPI takes the tag as it is (`0.5.2.0`). npm versions have three numbers and a release has four, so the fourth goes after a hyphen (`0.5.2.0` is `0.5.2-0`): npm calls such a version a pre-release, so install one exact version, the panel's, and do not use a range.

## Release

`release.yml` builds and publishes both on a stable tag only, after the release itself is published (a pre-release tag publishes none). The build job has no secret; the npm job publishes with the repository secret `NPM_TOKEN` and the provenance of the run, the PyPI job with trusted publishing. A failed publish job leaves the release as it is: rerun the failed jobs. A version of a package cannot be taken back or reused, so a bad one is replaced by the next release.

## What the maintainer sets up once

Nothing below is done by the repository; without it the publish jobs fail after the release is out.

1. **npm.** An npm account that owns the name `mikan-sdk` (it is free now; the first publish takes it). A token that may publish it, saved as the repository secret `NPM_TOKEN`: a granular access token with read and write on packages, or an automation token, because a token limited to named packages cannot name one that does not exist yet. After the first release it can be replaced by a token limited to `mikan-sdk`.
2. **PyPI.** A pending trusted publisher for the project `mikan-sdk` (pypi.org/manage/account/publishing): owner `Miroshka000`, repository `mikan`, workflow `release.yml`, environment `pypi`. No token is made.
3. **GitHub.** The environment `pypi` (Settings → Environments); GitHub makes an empty one on the first run, and rules such as a required reviewer can be added to it.
