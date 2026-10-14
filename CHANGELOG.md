# Changelog

## 0.2.0

### Compatibility

- Target CPA v8.0.28+ (native ABI 1, RPC schema 6). Older hosts should use v0.1.4.
- Preserve raw JSON management responses, including invite URLs containing `&`.
- Build Linux release libraries against GLIBC 2.17 and verify symbol requirements.

### Changes

- Read Codex credentials through native `host.auth.list` / `host.auth.get` callbacks instead of HTTP calls back to the management API.
- Select credentials by stable auth index; exclude disabled, unavailable, runtime-only and non-file credentials.
- Remove management-origin handling and internal management-key forwarding. Browser management requests still require authentication.
- Keep per-request invite proxy support. Credential reads do not force token refresh or inherit CPA credential proxy settings.
- Point plugin metadata to the active release repository.
- Add race tests and a native shared-library smoke test covering callback ownership, credential isolation, authentication guards and invite requests against a local mock server.
