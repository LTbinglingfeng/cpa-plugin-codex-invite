#!/usr/bin/env python3
"""Exercise the real Unix C ABI: python3 native-smoke.py /path/to/plugin.so (or .dylib)."""

import base64
import ctypes as C
import json
import os
from pathlib import Path
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer


class Buffer(C.Structure):
    _fields_ = [("ptr", C.c_void_p), ("len", C.c_size_t)]


HostCall = C.CFUNCTYPE(C.c_int, C.c_void_p, C.c_char_p, C.c_void_p,
                      C.c_size_t, C.POINTER(Buffer))
Free = C.CFUNCTYPE(None, C.c_void_p, C.c_size_t)
PluginCall = C.CFUNCTYPE(C.c_int, C.c_char_p, C.c_void_p, C.c_size_t,
                        C.POINTER(Buffer))
Shutdown = C.CFUNCTYPE(None)


class HostAPI(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("host_ctx", C.c_void_p),
                ("call", HostCall), ("free_buffer", Free)]


class PluginAPI(C.Structure):
    _fields_ = [("abi_version", C.c_uint32), ("call", PluginCall),
                ("free_buffer", Free), ("shutdown", Shutdown)]


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def encode(value):
    return json.dumps(value, separators=(",", ":")).encode()


def b64(raw):
    return base64.b64encode(raw).decode("ascii")


TOKEN = "native-smoke-access-token-DO-NOT-LEAK"
KEY = "native-smoke-management-key"
ACCOUNT = "native-smoke-account-id"
INDEX = "native-smoke-auth-index"
CALLBACK_ID = "native-smoke-callback-id"
ACCOUNTS = "/v0/management/codex-invite/accounts"
INVITE = "/v0/management/codex-invite/invite"
PUBLIC = "/v0/resource/plugins/codex-invite/invite"


class FakeHost:
    def __init__(self):
        self.buffers = {}
        self.errors = []
        self.calls = []
        self.allocated = 0
        self.freed = 0
        self.failure = None
        # Keep both CFUNCTYPE objects and every allocated buffer alive until freed.
        self.call_callback = HostCall(self.call)
        self.free_callback = Free(self.free)
        self.api = HostAPI(1, 0xCAFE, self.call_callback, self.free_callback)

    def call(self, ctx, method, request, length, response):
        # Exceptions cannot propagate through ctypes callbacks; record and fail later.
        try:
            check(ctx == 0xCAFE, "host_ctx did not round-trip")
            method = method.decode()
            payload = json.loads(C.string_at(request, length))
            self.calls.append((method, payload))
            check(payload.get("host_callback_id") == CALLBACK_ID,
                  "missing callback authorization context")
            check(KEY not in json.dumps(payload), "management key forwarded to callback")
            if method == "host.auth.list":
                valid = dict(name="codex-smoke.json", provider="codex", type="codex",
                             auth_index=INDEX, source="file", email="owner@example.com",
                             status="active", access_token=TOKEN)
                files = [valid]
                for override in ({"provider": "other"}, {"disabled": True},
                                 {"unavailable": True}, {"runtime_only": True},
                                 {"source": "runtime"}, {"auth_index": ""},
                                 {"name": "not-json.txt"}):
                    files.append(dict(valid, **override))
                result = {"files": files}
            elif method == "host.auth.get":
                check(payload.get("auth_index") == INDEX, "wrong credential selected")
                result = {"auth_index": INDEX, "name": "codex-smoke.json",
                          "json": {"type": "codex", "access_token": TOKEN,
                                   "account_id": ACCOUNT, "email": "owner@example.com"}}
            else:
                raise AssertionError("unexpected callback: " + method)
            envelope, code = {"ok": True, "result": result}, 0
            if self.failure == (method, "error"):
                envelope = {"ok": False, "error": {"code": "denied", "message": TOKEN}}
            elif self.failure == (method, "code"):
                code = 23  # A success envelope must not override a failing native code.
            raw = encode(envelope)
            buf = C.create_string_buffer(raw)
            address = C.addressof(buf)
            # Advertise an oversized buffer without allocating it: the plugin
            # must reject the length before reading and still call free_buffer.
            size = (16 << 20) + 1 if self.failure == (method, "large") else len(raw)
            self.buffers[address] = (buf, size)
            self.allocated += 1
            response.contents.ptr = address
            response.contents.len = size
            return code
        except BaseException as exc:
            self.errors.append(str(exc))
            return 99

    def free(self, ptr, length):
        try:
            check(ptr in self.buffers, "unknown or double-freed host buffer")
            _, expected = self.buffers.pop(ptr)
            check(length == expected, "host free length mismatch")
            self.freed += 1
        except BaseException as exc:
            self.errors.append(str(exc))

    def verify(self):
        check(not self.errors, "callback errors: " + repr(self.errors))
        check(not self.buffers, "host buffers leaked")
        check(self.allocated == self.freed, "host allocation/free counts differ")


def run(path):
    # Loopback must not be routed through a CI runner's outbound proxy.
    os.environ["NO_PROXY"] = os.environ["no_proxy"] = "127.0.0.1,localhost"
    received = []
    invite_url = "https://example.invalid/invite?id=smoke&email=guest%40example.com"

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            received.append((self.path, dict(self.headers), body))
            raw = encode({"invites": [{"email": "guest@example.com",
                                       "invite_url": invite_url, "referral_id": "smoke"}]})
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.send_header("x-oai-request-id", "native-smoke-request")
            self.end_headers()
            self.wfile.write(raw)

        def log_message(self, *_args):
            pass

    host = FakeHost()
    lib = C.CDLL(str(path))
    init = lib.cliproxy_plugin_init
    init.argtypes = [C.POINTER(HostAPI), C.POINTER(PluginAPI)]
    init.restype = C.c_int
    api = PluginAPI()
    check(init(None, C.byref(api)) != 0, "NULL host accepted")
    check(init(C.byref(host.api), None) != 0, "NULL plugin accepted")
    for invalid in (HostAPI(0, None, host.call_callback, host.free_callback),
                    HostAPI(999, None, host.call_callback, host.free_callback),
                    HostAPI(1, None, HostCall(), host.free_callback),
                    HostAPI(1, None, host.call_callback, Free())):
        check(init(C.byref(invalid), C.byref(api)) != 0, "invalid host API accepted")
    check(init(C.byref(host.api), C.byref(api)) == 0, "ABI initialization failed")
    check(api.abi_version == 1 and api.call and api.free_buffer and api.shutdown,
          "invalid returned plugin API")

    def call(method, payload, success=True):
        raw = encode(payload)
        request = C.create_string_buffer(raw)
        response = Buffer()
        try:
            code = api.call(method.encode(), request, len(raw), C.byref(response))
            check(response.ptr and response.len, "empty plugin response")
            data = C.string_at(response.ptr, response.len)
            check(TOKEN.encode() not in data, "token leaked in ABI response")
            env = json.loads(data)
            check((code == 0) == success, "unexpected native return code: " + str(code))
            check(env.get("ok") is success, "unexpected envelope: " + repr(env))
            return env.get("result") if success else env
        finally:
            if response.ptr:
                api.free_buffer(response.ptr, response.len)
            host.verify()

    def management(path, body=None, authorized=True, status=200):
        payload = {"Method": "GET" if body is None else "POST", "Path": path,
                   "Headers": {"Authorization": ["Bearer " + KEY]} if authorized else {},
                   "host_callback_id": CALLBACK_ID}
        if body is not None:
            payload["Body"] = b64(encode(body))
        result = call("management.handle", payload)
        check(result["StatusCode"] == status, "wrong HTTP status: " + repr(result))
        raw = base64.b64decode(result["Body"], validate=True)
        check(TOKEN.encode() not in raw and KEY.encode() not in raw,
              "secret leaked in decoded management body")
        if path == PUBLIC:
            check("text/html" in result["Headers"]["Content-Type"][0], "not HTML")
            return raw.decode()
        return json.loads(raw)

    server = HTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        rejected = call("plugin.register", {"schema_version": 1}, success=False)
        check("schema" in rejected["error"]["message"].lower(), "wrong schema error")
        config = "base_url: http://127.0.0.1:%d\n" % server.server_port
        registration = call("plugin.register", {"schema_version": 6,
                                                "config_yaml": b64(config.encode())})
        check(registration["schema_version"] == 6, "wrong registration schema")
        check(registration["capabilities"]["management_api"], "missing management capability")
        routes = call("management.register", {})
        check({(r["Method"], r["Path"]) for r in routes["routes"]} ==
              {("GET", "/codex-invite/accounts"), ("POST", "/codex-invite/invite")},
              "wrong management routes")
        check(any(r["Path"] == "/invite" for r in routes["resources"]), "missing resource")
        before = len(host.calls)
        html = management(PUBLIC, authorized=False)
        check("<html" in html.lower() and "Codex Invite" in html, "invalid resource page")
        invite = {"auth_index": INDEX, "emails": ["guest@example.com"]}
        management(ACCOUNTS, authorized=False, status=401)
        management(INVITE, invite, authorized=False, status=401)
        check(len(host.calls) == before, "public/unauthorized request accessed credentials")
        accounts = management(ACCOUNTS)["accounts"]
        check(len(accounts) == 1 and accounts[0]["auth_index"] == INDEX,
              "account filtering failed")
        check([m for m, _ in host.calls[before:]] == ["host.auth.list"],
              "account listing fetched credentials")
        before = len(host.calls)
        result = management(INVITE, invite)
        check([m for m, _ in host.calls[before:]] == ["host.auth.list", "host.auth.get"],
              "invite callback sequence incorrect")
        check(result["ok"] and result["status_code"] == 200, "invite failed")
        check(result["invites"][0]["invite_url"] == invite_url,
              "ampersand URL corrupted/HTML-escaped")
        check(result["upstream"]["invites"][0]["invite_url"] == invite_url,
              "upstream URL corrupted")
        check(len(received) == 1, "wrong number of upstream requests")
        target, headers, body = received[0]
        headers = {k.lower(): v for k, v in headers.items()}
        check(target == "/backend-api/wham/referrals/invite", "wrong upstream path")
        check(headers.get("authorization") == "Bearer " + TOKEN, "wrong upstream token")
        check(headers.get("chatgpt-account-id") == ACCOUNT, "missing account header")
        check(KEY not in json.dumps(headers) and KEY.encode() not in body,
              "management key leaked upstream")
        check(json.loads(body) == {"emails": ["guest@example.com"],
                                  "referral_key": "codex_referral_persistent_invite"},
              "wrong upstream payload")
        for method in ("host.auth.list", "host.auth.get"):
            for failure in ("error", "code", "large"):
                host.failure = (method, failure)
                before = len(host.calls)
                error = management(INVITE, invite, status=502)
                check(method in error["error"], "callback error not surfaced")
                if failure == "code":
                    check("code=23" in error["error"], "native host error ignored")
                if failure == "large":
                    check("too large" in error["error"], "oversized response accepted")
                expected = ["host.auth.list"] if method.endswith("list") else ["host.auth.list", "host.auth.get"]
                check([m for m, _ in host.calls[before:]] == expected,
                      "callback failure did not stop processing")
                check(len(received) == 1, "callback failure reached upstream")
        host.failure = None
        management(ACCOUNTS)  # Failure injection must not poison subsequent calls.
        call("plugin.shutdown", {})
    finally:
        api.shutdown()
        server.shutdown()
        thread.join(timeout=5)
        server.server_close()
        host.verify()
    print("PASS: native C ABI, schema/routes, public/auth guards, accounts/invite, "
          "secret isolation, callback errors; %d/%d host buffers freed" %
          (host.freed, host.allocated))


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: python3 native-smoke.py /path/to/codex-invite.{dylib,so}")
    try:
        run(Path(sys.argv[1]).expanduser().resolve(strict=True))
    except Exception as exc:
        sys.exit("FAIL: " + str(exc))
