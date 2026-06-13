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

```yaml
plugins:
  enabled: true
  configs:
    codex-invite:
      enabled: true
      priority: 1
      referral_key: "codex_referral_persistent_invite"
      max_emails_per_request: 10
```

Supported plugin config fields:

- `referral_key`: invite referral key. Defaults to `codex_referral_persistent_invite`.
- `max_emails_per_request`: safety limit for one send request. Defaults to `10`.
- `base_url`: ChatGPT base URL. Defaults to `https://chatgpt.com`.
- `language`: `oai-language` header. Defaults to `zh-CN`.
- `originator`: `originator` header. Defaults to `Codex Desktop`.
- `user_agent`: upstream user agent. Defaults to a Codex Desktop-like browser UA.
- `cookie`: optional upstream Cookie header. Prefer entering this per request only when required.

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
- Plugin config loading and saving through `GET/PATCH /v0/management/plugins/codex-invite/config`.
- Invite execution through `POST /v0/management/codex-invite/invite`.

The page does not store the CPA management key or Cookie in `localStorage`.
Saving the Cookie into plugin config only happens when `Update saved cookie` is
checked; loading plugin config never writes the saved Cookie back into the
visible textarea.

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
