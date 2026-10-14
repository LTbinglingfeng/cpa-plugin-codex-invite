# Codex Invite CLIProxyAPI Plugin

`codex-invite` is a CLIProxyAPI dynamic library plugin that exposes a
Management UI resource for sending Codex referral invite emails with an
existing Codex OAuth credential managed by CPA.

The plugin does not persist ChatGPT access tokens. At send time it reads the
selected Codex auth file through CPA's native `host.auth.list` / `host.auth.get`
callbacks, extracts the stored `access_token` and account ID, and calls:

```text
POST https://chatgpt.com/backend-api/wham/referrals/invite
```

## Compatibility

Version 0.2.0 targets **CPA v8.0.28 or newer**, using native ABI 1 and RPC
schema 6. Older CPA installations should keep plugin v0.1.4 or upgrade CPA first.
Use a CPA build with dynamic plugin support: `_no-plugin` builds cannot load it.
Linux release libraries target GLIBC 2.17 or newer, matching CPA's Linux baseline;
musl-only systems are not supported by these libraries.

Only enabled, available, file-backed Codex credentials with an `auth_index` are
selectable. Runtime-only credentials cannot be read through `host.auth.get`.
Reading a credential does **not** force a token refresh; it reads the latest
stored file, which CPA may update through its background refresh process.

## Configuration

The plugin does not expose invite fields in the Management Center plugin
configuration form. Plugin config is only used to enable the plugin:

```yaml
plugins:
  enabled: true
  configs:
    codex-invite:
      enabled: true
      priority: 1
```

## Resource Page

The plugin resource page is available at:

```text
/v0/resource/plugins/codex-invite/invite
```

It provides:

- CPA management key entry for authenticated Management API calls.
- Codex credential loading and account selection through CPA host callbacks.
- Invite settings for referral key, ChatGPT base URL, language, originator, user agent, request email limit, and optional Cookie.
- A visible per-request proxy URL field in the invite form.
- Local browser settings for non-secret fields, excluding proxy URL.
- Invite execution through `POST /v0/management/codex-invite/invite`.

The page does not store the CPA management key, proxy URL, or Cookie in `localStorage`.
Invite details and account choice are entered in this custom page, not in the
plugin configuration form.

## Build

```bash
make test
make build
make package
```

On macOS this creates:

```text
dist/codex-invite.dylib
dist/codex-invite_0.2.0_darwin_arm64.zip
dist/codex-invite_0.2.0_darwin_arm64.zip.sha256
```

Install locally by copying the dynamic library to CPA's plugin discovery
directory, for example:

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/darwin/arm64
cp dist/codex-invite.dylib /path/to/CLIProxyAPI/plugins/darwin/arm64/codex-invite.dylib
```

Target platform, output directory, and runtime plugin version can be overridden:

```bash
make build GOOS=darwin GOARCH=arm64 BUILD_DIR=/path/to/plugins/darwin/arm64
make package VERSION=0.2.0
```

## Plugin Store Release

For plugin-store installation, each GitHub release must include:

```text
codex-invite_<version>_<goos>_<goarch>.zip
checksums.txt
```

Each zip must contain the dynamic library at the zip root:

- Darwin: `codex-invite.dylib`
- Linux: `codex-invite.so`
- Windows: `codex-invite.dll`

`checksums.txt` must be in sha256sum format.

Generate a local aggregate checksum file with:

```bash
make checksums VERSION=0.2.0
```

## Management API

The plugin registers:

- `GET /v0/management/codex-invite/accounts`
- `POST /v0/management/codex-invite/invite`
- resource page `/v0/resource/plugins/codex-invite/invite`

The resource page asks for the CPA management key because plugin iframes are
served from the CPA backend origin and cannot read the Management Center's
frontend auth store. The browser still authenticates every management request;
host callbacks remove only the plugin's internal HTTP loopback and management
key forwarding. The public resource route serves HTML only, never credentials
or invite execution.

Invite HTTP requests retain the per-request proxy field (HTTP, HTTPS, SOCKS5,
or SOCKS5H). An empty field uses Go's default HTTP client and its environment
proxy behavior, not CPA's global or per-credential proxy settings.

## Release

Releases are published from
[`LTbinglingfeng/cpa-plugin-codex-invite`](https://github.com/LTbinglingfeng/cpa-plugin-codex-invite).
After tests pass, pushing a `v*` tag triggers `.github/workflows/build.yml`,
which builds the platform archives and publishes them with `checksums.txt`.
Linux artifacts are built in manylinux2014 containers and checked for a maximum
GLIBC requirement of 2.17. Local `make build` uses the local system's C toolchain
and does not by itself guarantee this compatibility baseline.
