package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func mockHost(t *testing.T, fn func(string, any) (json.RawMessage, error)) {
	t.Helper()
	previous := hostCall
	hostCall = fn
	t.Cleanup(func() { hostCall = previous })
}

func TestHostAccountsFilteringAndSorting(t *testing.T) {
	files := []pluginapi.HostAuthFileEntry{
		{Provider: "codex", Name: "z.json", AuthIndex: "z", Source: "file", Email: "z@example.com"},
		{Type: "CODEX", Name: "a.json", AuthIndex: "a", Source: "file", Email: "a@example.com"},
	}
	for _, mutate := range []func(*pluginapi.HostAuthFileEntry){
		func(f *pluginapi.HostAuthFileEntry) { f.Provider = "claude" },
		func(f *pluginapi.HostAuthFileEntry) { f.Disabled = true },
		func(f *pluginapi.HostAuthFileEntry) { f.Unavailable = true },
		func(f *pluginapi.HostAuthFileEntry) { f.RuntimeOnly = true },
		func(f *pluginapi.HostAuthFileEntry) { f.AuthIndex = "" },
		func(f *pluginapi.HostAuthFileEntry) { f.Source = "memory" },
		func(f *pluginapi.HostAuthFileEntry) { f.Name = "no-json" },
	} {
		f := files[0]
		mutate(&f)
		files = append(files, f)
	}
	mockHost(t, func(method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostAuthList {
			t.Fatalf("method = %s", method)
		}
		raw, _ := json.Marshal(payload)
		if string(raw) != `{"host_callback_id":"callback-1"}` {
			t.Fatalf("payload = %s", raw)
		}
		return json.Marshal(map[string]any{"files": files})
	})
	req := managementRequest{HostCallbackID: "callback-1", ManagementRequest: pluginapi.ManagementRequest{Headers: http.Header{"Authorization": {"Bearer management-secret"}}}}
	got, err := fetchCodexAccounts(req)
	if err != nil || len(got) != 2 {
		t.Fatalf("accounts=%+v error=%v", got, err)
	}
	if got[0].AuthIndex != "a" || got[1].AuthIndex != "z" {
		t.Fatalf("order=%+v", got)
	}
}

func TestHostCredentialUsesIndexAndStoredJSON(t *testing.T) {
	mockHost(t, func(method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostAuthGet {
			t.Fatalf("method=%s", method)
		}
		raw, _ := json.Marshal(payload)
		var fields map[string]string
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 2 || fields["auth_index"] != "index-1" || fields["host_callback_id"] != "callback-1" {
			t.Fatalf("payload=%s", raw)
		}
		return json.RawMessage(`{"auth_index":"index-1","json":{"access_token":"stored-token","account_id":"account-1","email":"owner@example.com"}}`), nil
	})
	got, err := fetchCodexCredential(managementRequest{HostCallbackID: "callback-1"}, accountInfo{AuthIndex: "index-1", Name: "ignored.json"})
	if err != nil || got.AccessToken != "stored-token" || got.AccountID != "account-1" {
		t.Fatalf("credential=%+v error=%v", got, err)
	}
}

func TestHostCredentialRejectsInvalidResponses(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		hostErr   error
	}{
		{name: "callback failure", hostErr: errors.New("host unavailable")},
		{name: "malformed", raw: `{`},
		{name: "wrong index", raw: `{"auth_index":"other","json":{"access_token":"secret"}}`},
		{name: "missing token", raw: `{"auth_index":"index-1","json":{"account_id":"account-1"}}`},
		{name: "missing JSON", raw: `{"auth_index":"index-1"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mockHost(t, func(string, any) (json.RawMessage, error) { return json.RawMessage(tt.raw), tt.hostErr })
			if _, err := fetchCodexCredential(managementRequest{}, accountInfo{AuthIndex: "index-1"}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestAccountSelectionDoesNotFallBackFromIndex(t *testing.T) {
	accounts := []accountInfo{{AuthIndex: "index-1", Name: "one.json"}}
	if _, err := selectAccount(accounts, inviteRequest{AuthIndex: "missing", AuthName: "one.json"}); err == nil {
		t.Fatal("stale index selected by name")
	}
	if _, err := selectAccount(accounts, inviteRequest{AuthName: "one.json"}); err != nil {
		t.Fatal(err)
	}
}

func TestManagementRequiresKeyAndResourceDoesNotReadCredentials(t *testing.T) {
	calls := 0
	mockHost(t, func(string, any) (json.RawMessage, error) { calls++; return json.RawMessage(`{"files":[]}`), nil })
	if got := handleAccounts(managementRequest{}); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("accounts status=%d", got.StatusCode)
	}
	if got := handleInvite(managementRequest{}); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invite status=%d", got.StatusCode)
	}
	raw, _ := json.Marshal(managementRequest{ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: resourceInvitePath}})
	if _, err := handleManagement(raw); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("public/unauthenticated requests made %d callbacks", calls)
	}
	got := handleAccounts(managementRequest{ManagementRequest: pluginapi.ManagementRequest{Headers: http.Header{"X-Management-Key": {"key"}}}})
	if got.StatusCode != http.StatusOK || calls != 1 {
		t.Fatalf("alternate key status=%d calls=%d", got.StatusCode, calls)
	}
}

func TestHostAccountsErrors(t *testing.T) {
	for _, raw := range []string{`{`, `{"files":"invalid"}`} {
		t.Run(raw, func(t *testing.T) {
			mockHost(t, func(string, any) (json.RawMessage, error) { return json.RawMessage(raw), nil })
			if _, err := fetchCodexAccounts(managementRequest{}); err == nil {
				t.Fatal("expected malformed list error")
			}
		})
	}
}

func TestSchemaAndLifecycle(t *testing.T) {
	t.Cleanup(func() { activeConfig.Store(defaultConfig()) })
	for _, schema := range []uint32{0, 1, 5, 6, 7} {
		request, _ := json.Marshal(lifecycleRequest{SchemaVersion: schema})
		_, err := handleMethod(pluginabi.MethodPluginRegister, request)
		if (err == nil) != (schema >= 6) {
			t.Fatalf("schema=%d error=%v", schema, err)
		}
	}
	if got := pluginRegistration(); got.SchemaVersion != 6 || got.Metadata.Version != "0.2.0" {
		t.Fatalf("registration=%+v", got)
	}
	for _, method := range []string{pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown} {
		raw, err := handleMethod(method, nil)
		if err != nil || !strings.Contains(string(raw), `"ok":true`) {
			t.Fatalf("%s: %s %v", method, raw, err)
		}
	}
}

func TestResourcePageDoesNotForwardManagementOrigin(t *testing.T) {
	page := renderInvitePage(defaultConfig())
	for _, field := range []string{"management_origin", "X-Codex-Invite-Origin"} {
		if strings.Contains(page, field) {
			t.Fatalf("obsolete field %s", field)
		}
	}
	if !strings.Contains(page, `'Authorization': authorization`) {
		t.Fatal("browser authentication missing")
	}
}

func TestUninitializedHostCallbackFailsSafely(t *testing.T) {
	if _, err := callHost(pluginabi.MethodHostAuthList, struct{}{}); err == nil {
		t.Fatal("expected unavailable host error")
	}
}

func TestJSONResponsePreservesInviteURL(t *testing.T) {
	want := "https://chatgpt.com/invite?a=1&b=2"
	response := jsonResponse(http.StatusOK, inviteResponse{Invites: []inviteLink{{InviteURL: want}}})
	var got inviteResponse
	if err := json.Unmarshal(response.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Invites[0].InviteURL != want {
		t.Fatalf("URL=%s", got.Invites[0].InviteURL)
	}
}
