package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestCollectEmailsSplitsDedupesAndValidates(t *testing.T) {
	emails, err := collectEmails(inviteRequest{
		Emails:     []string{"User@example.com,second@example.com"},
		EmailsText: "user@example.com\nthird@example.com",
	}, 10)
	if err != nil {
		t.Fatalf("collectEmails() error = %v", err)
	}
	want := []string{"User@example.com", "second@example.com", "third@example.com"}
	if !reflect.DeepEqual(emails, want) {
		t.Fatalf("emails = %#v, want %#v", emails, want)
	}
}

func TestCollectEmailsRejectsInvalidAndTooMany(t *testing.T) {
	if _, err := collectEmails(inviteRequest{EmailsText: "not-an-email"}, 10); err == nil {
		t.Fatal("collectEmails() error = nil, want invalid email error")
	}
	if _, err := collectEmails(inviteRequest{EmailsText: "a@example.com b@example.com"}, 1); err == nil {
		t.Fatal("collectEmails() error = nil, want max email error")
	}
}

func TestNormalizeOrigin(t *testing.T) {
	got, err := normalizeOrigin("https://127.0.0.1:8317/some/path?x=1")
	if err != nil {
		t.Fatalf("normalizeOrigin() error = %v", err)
	}
	if got != "https://127.0.0.1:8317" {
		t.Fatalf("origin = %q, want https://127.0.0.1:8317", got)
	}
}

func TestInviteEndpoint(t *testing.T) {
	got, err := inviteEndpoint("https://chatgpt.com/")
	if err != nil {
		t.Fatalf("inviteEndpoint() error = %v", err)
	}
	want := "https://chatgpt.com/backend-api/wham/referrals/invite"
	if got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestParseCodexCredential(t *testing.T) {
	credential, err := parseCodexCredential([]byte(`{
		"type": "codex",
		"access_token": "access-1",
		"account_id": "account-1",
		"email": "user@example.com"
	}`))
	if err != nil {
		t.Fatalf("parseCodexCredential() error = %v", err)
	}
	if credential.AccessToken != "access-1" || credential.AccountID != "account-1" || credential.Email != "user@example.com" {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestParseCodexCredentialTokenDataFallback(t *testing.T) {
	credential, err := parseCodexCredential([]byte(`{
		"token_data": {
			"access_token": "access-2",
			"account_id": "account-2",
			"email": "fallback@example.com"
		}
	}`))
	if err != nil {
		t.Fatalf("parseCodexCredential() error = %v", err)
	}
	if credential.AccessToken != "access-2" || credential.AccountID != "account-2" || credential.Email != "fallback@example.com" {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestRenderInvitePageEscapesDefaults(t *testing.T) {
	cfg := defaultConfig()
	cfg.ReferralKey = `</script><img src=x onerror=alert(1)>`

	page := renderInvitePage(cfg)
	if strings.Contains(page, cfg.ReferralKey) {
		t.Fatalf("page contains unescaped referral key")
	}
	if !strings.Contains(page, `\u003c/script\u003e`) {
		t.Fatalf("page does not contain JSON-escaped referral key")
	}
}

func TestRenderInvitePageCollapsesSettingsAndIncludesI18n(t *testing.T) {
	page := renderInvitePage(defaultConfig())
	if !strings.Contains(page, `<details class="panel collapsible" id="settingsPanel">`) {
		t.Fatalf("page does not render Settings as a collapsed details card")
	}
	if strings.Contains(page, `<details class="panel collapsible" id="settingsPanel" open>`) {
		t.Fatalf("settings details card is open by default")
	}
	for _, want := range []string{
		`id="localeSelect"`,
		`data-i18n="settings.title"`,
		`'settings.title': 'Settings'`,
		`'settings.title': '设置'`,
		`'invite.send': 'Send invites'`,
		`'invite.send': '发送邀请'`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("page is missing %q", want)
		}
	}
}

func TestRegistrationUsesCustomPageInsteadOfConfigFields(t *testing.T) {
	reg := pluginRegistration()
	if len(reg.Metadata.ConfigFields) != 0 {
		t.Fatalf("config fields = %#v, want none", reg.Metadata.ConfigFields)
	}

	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("handleMethod(MethodManagementRegister) error = %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope ok = false, error = %#v", env.Error)
	}

	var registration managementRegistrationResponse
	if err := json.Unmarshal(env.Result, &registration); err != nil {
		t.Fatalf("decode management registration: %v", err)
	}
	if len(registration.Resources) != 1 {
		t.Fatalf("resources = %#v, want one custom page", registration.Resources)
	}
	if got := registration.Resources[0]; got.Path != "/invite" || got.Menu != "Codex Invite" {
		t.Fatalf("resource = %#v, want /invite Codex Invite", got)
	}

	routes := map[string]bool{}
	for _, route := range registration.Routes {
		routes[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		http.MethodGet + " /codex-invite/accounts",
		http.MethodPost + " /codex-invite/invite",
	} {
		if !routes[want] {
			t.Fatalf("registered routes = %#v, missing %s", registration.Routes, want)
		}
	}
}
