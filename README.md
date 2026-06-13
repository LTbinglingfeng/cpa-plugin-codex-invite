# Codex Invite CLIProxyAPI Plugin

`codex-invite` is a CLIProxyAPI dynamic library plugin that exposes a
Management UI resource for sending Codex referral invite emails with an
existing Codex OAuth credential managed by CPA.

The plugin does not persist ChatGPT access tokens. At send time it reads the
selected Codex auth file through CPA's authenticated Management API, extracts
the current `access_token` and account ID, and calls:

```text
POST https://chatgpt.com/backend-api/wham/referrals/invite
```

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
- Codex credential loading and account selection from CPA auth files.
- Invite settings for referral key, ChatGPT base URL, language, originator, user agent, request email limit, and optional Cookie.
- Local browser settings for non-secret fields.
- Invite execution through `POST /v0/management/codex-invite/invite`.

The page does not store the CPA management key or Cookie in `localStorage`.
Invite details and account choice are entered in this custom page, not in the
plugin configuration form.

## Build

```bash
make test
make build
```

On macOS this creates:

```text
dist/codex-invite.dylib
```

Install locally by copying the dynamic library to CPA's plugin discovery
directory, for example:

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/darwin/arm64
cp dist/codex-invite.dylib /path/to/CLIProxyAPI/plugins/darwin/arm64/codex-invite.dylib
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

## Management API

The plugin registers:

- `GET /v0/management/codex-invite/accounts`
- `POST /v0/management/codex-invite/invite`
- resource page `/v0/resource/plugins/codex-invite/invite`

The resource page asks for the CPA management key because plugin iframes are
served from the CPA backend origin and cannot read the Management Center's
frontend auth store.
