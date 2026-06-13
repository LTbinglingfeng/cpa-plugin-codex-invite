package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginID                      = "codex-invite"
	defaultReferralKey            = "codex_referral_persistent_invite"
	defaultBaseURL                = "https://chatgpt.com"
	defaultLanguage               = "zh-CN"
	defaultOriginator             = "Codex Desktop"
	defaultUserAgent              = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
	defaultMaxEmails              = 10
	upperMaxEmails                = 50
	maxManagementBodyBytes        = 1 << 20
	managementAccountsPath        = "/v0/management/codex-invite/accounts"
	managementInvitePath          = "/v0/management/codex-invite/invite"
	resourceInvitePath            = "/v0/resource/plugins/codex-invite/invite"
	authFilesPath                 = "/v0/management/auth-files"
	authFileDownloadPath          = "/v0/management/auth-files/download"
	inviteEndpointPath            = "/backend-api/wham/referrals/invite"
	requestManagementOrigin       = "X-Codex-Invite-Origin"
	contentTypeJSON               = "application/json; charset=utf-8"
	contentTypeHTML               = "text/html; charset=utf-8"
	upstreamBodyLimit       int64 = 1 << 20
)

var pluginVersion = "0.1.4"

var (
	activeConfig atomic.Value
	emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

func init() {
	activeConfig.Store(defaultConfig())
}

func main() {}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

type managementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

type managementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type pluginConfig struct {
	ReferralKey         string `yaml:"referral_key"`
	BaseURL             string `yaml:"base_url"`
	Language            string `yaml:"language"`
	Originator          string `yaml:"originator"`
	UserAgent           string `yaml:"user_agent"`
	Cookie              string `yaml:"cookie"`
	MaxEmailsPerRequest int    `yaml:"max_emails_per_request"`
}

type accountInfo struct {
	AuthIndex        string `json:"auth_index,omitempty"`
	Name             string `json:"name"`
	Label            string `json:"label,omitempty"`
	Email            string `json:"email,omitempty"`
	Account          string `json:"account,omitempty"`
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	Status           string `json:"status,omitempty"`
	Source           string `json:"source,omitempty"`
}

type accountsResponse struct {
	Accounts []accountInfo `json:"accounts"`
}

type inviteRequest struct {
	AuthIndex           string   `json:"auth_index,omitempty"`
	AuthName            string   `json:"auth_name,omitempty"`
	Emails              []string `json:"emails,omitempty"`
	EmailsText          string   `json:"emails_text,omitempty"`
	ReferralKey         string   `json:"referral_key,omitempty"`
	BaseURL             string   `json:"base_url,omitempty"`
	ProxyURL            string   `json:"proxy_url,omitempty"`
	Language            string   `json:"language,omitempty"`
	Originator          string   `json:"originator,omitempty"`
	UserAgent           string   `json:"user_agent,omitempty"`
	Cookie              string   `json:"cookie,omitempty"`
	MaxEmailsPerRequest int      `json:"max_emails_per_request,omitempty"`
	ManagementOrigin    string   `json:"management_origin,omitempty"`
}

type inviteLink struct {
	ReferralID string `json:"referral_id,omitempty"`
	Email      string `json:"email,omitempty"`
	InviteURL  string `json:"invite_url,omitempty"`
}

type inviteResponse struct {
	OK          bool         `json:"ok"`
	StatusCode  int          `json:"status_code"`
	RequestID   string       `json:"request_id,omitempty"`
	Account     accountInfo  `json:"account"`
	Emails      []string     `json:"emails"`
	ReferralKey string       `json:"referral_key"`
	Invites     []inviteLink `json:"invites,omitempty"`
	Upstream    any          `json:"upstream,omitempty"`
	UpstreamRaw string       `json:"upstream_raw,omitempty"`
}

type codexCredential struct {
	AccessToken string
	AccountID   string
	Email       string
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistrationResponse{
			Routes: []pluginapi.ManagementRoute{
				{Method: http.MethodGet, Path: "/codex-invite/accounts"},
				{Method: http.MethodPost, Path: "/codex-invite/invite"},
			},
			Resources: []pluginapi.ResourceRoute{{
				Path:        "/invite",
				Menu:        "Codex Invite",
				Description: "Send Codex invite emails with a selected Codex credential.",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}

	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		var decoded pluginConfig
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &decoded); errUnmarshal != nil {
			return errUnmarshal
		}
		cfg = mergeConfig(cfg, decoded)
	}
	activeConfig.Store(normalizeConfig(cfg))
	return nil
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		ReferralKey:         defaultReferralKey,
		BaseURL:             defaultBaseURL,
		Language:            defaultLanguage,
		Originator:          defaultOriginator,
		UserAgent:           defaultUserAgent,
		MaxEmailsPerRequest: defaultMaxEmails,
	}
}

func mergeConfig(base, override pluginConfig) pluginConfig {
	if strings.TrimSpace(override.ReferralKey) != "" {
		base.ReferralKey = override.ReferralKey
	}
	if strings.TrimSpace(override.BaseURL) != "" {
		base.BaseURL = override.BaseURL
	}
	if strings.TrimSpace(override.Language) != "" {
		base.Language = override.Language
	}
	if strings.TrimSpace(override.Originator) != "" {
		base.Originator = override.Originator
	}
	if strings.TrimSpace(override.UserAgent) != "" {
		base.UserAgent = override.UserAgent
	}
	if strings.TrimSpace(override.Cookie) != "" {
		base.Cookie = override.Cookie
	}
	if override.MaxEmailsPerRequest != 0 {
		base.MaxEmailsPerRequest = override.MaxEmailsPerRequest
	}
	return base
}

func normalizeConfig(cfg pluginConfig) pluginConfig {
	cfg.ReferralKey = strings.TrimSpace(cfg.ReferralKey)
	if cfg.ReferralKey == "" {
		cfg.ReferralKey = defaultReferralKey
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.Language = strings.TrimSpace(cfg.Language)
	if cfg.Language == "" {
		cfg.Language = defaultLanguage
	}
	cfg.Originator = strings.TrimSpace(cfg.Originator)
	if cfg.Originator == "" {
		cfg.Originator = defaultOriginator
	}
	cfg.UserAgent = strings.TrimSpace(cfg.UserAgent)
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	cfg.Cookie = strings.TrimSpace(cfg.Cookie)
	if cfg.MaxEmailsPerRequest <= 0 {
		cfg.MaxEmailsPerRequest = defaultMaxEmails
	}
	if cfg.MaxEmailsPerRequest > upperMaxEmails {
		cfg.MaxEmailsPerRequest = upperMaxEmails
	}
	return cfg
}

func currentConfig() pluginConfig {
	raw := activeConfig.Load()
	if cfg, ok := raw.(pluginConfig); ok {
		return cfg
	}
	return defaultConfig()
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Codex Invite",
			Version:          pluginVersion,
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/cpa-plugin-codex-invite",
		},
		Capabilities: registrationCapabilities{ManagementAPI: true},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	if path == "" {
		path = "/"
	}

	switch {
	case strings.EqualFold(req.Method, http.MethodGet) && path == resourceInvitePath:
		return okEnvelope(htmlResponse(http.StatusOK, renderInvitePage(currentConfig())))
	case strings.EqualFold(req.Method, http.MethodGet) && path == managementAccountsPath:
		return okEnvelope(handleAccounts(req.ManagementRequest))
	case strings.EqualFold(req.Method, http.MethodPost) && path == managementInvitePath:
		return okEnvelope(handleInvite(req.ManagementRequest))
	default:
		return okEnvelope(jsonResponse(http.StatusNotFound, map[string]any{"error": "plugin route not found"}))
	}
}

func handleAccounts(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	accounts, errAccounts := fetchCodexAccounts(req, "")
	if errAccounts != nil {
		return jsonResponse(statusForError(errAccounts), map[string]any{"error": errAccounts.Error()})
	}
	return jsonResponse(http.StatusOK, accountsResponse{Accounts: accounts})
}

func handleInvite(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	if len(req.Body) > maxManagementBodyBytes {
		return jsonResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": "request body is too large"})
	}
	var payload inviteRequest
	if errUnmarshal := json.Unmarshal(req.Body, &payload); errUnmarshal != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid JSON request body"})
	}

	cfg := currentConfig()
	requestCfg := mergeConfig(cfg, pluginConfig{
		BaseURL:    payload.BaseURL,
		Language:   payload.Language,
		Originator: payload.Originator,
		UserAgent:  payload.UserAgent,
	})
	requestCfg = normalizeConfig(requestCfg)

	maxEmails := cfg.MaxEmailsPerRequest
	if payload.MaxEmailsPerRequest > 0 && payload.MaxEmailsPerRequest < maxEmails {
		maxEmails = payload.MaxEmailsPerRequest
	}
	emails, errEmails := collectEmails(payload, maxEmails)
	if errEmails != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errEmails.Error()})
	}

	accounts, errAccounts := fetchCodexAccounts(req, payload.ManagementOrigin)
	if errAccounts != nil {
		return jsonResponse(statusForError(errAccounts), map[string]any{"error": errAccounts.Error()})
	}
	account, errAccount := selectAccount(accounts, payload)
	if errAccount != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": errAccount.Error()})
	}

	credential, errCredential := fetchCodexCredential(req, payload.ManagementOrigin, account)
	if errCredential != nil {
		return jsonResponse(statusForError(errCredential), map[string]any{"error": errCredential.Error()})
	}
	if credential.AccountID == "" {
		credential.AccountID = account.ChatGPTAccountID
	}
	if credential.Email == "" {
		credential.Email = account.Email
	}

	referralKey := strings.TrimSpace(payload.ReferralKey)
	if referralKey == "" {
		referralKey = requestCfg.ReferralKey
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, errSend := sendInvite(ctx, requestCfg, credential, account, emails, referralKey, strings.TrimSpace(payload.Cookie), strings.TrimSpace(payload.ProxyURL))
	if errSend != nil {
		return jsonResponse(statusForError(errSend), map[string]any{"error": errSend.Error()})
	}
	return jsonResponse(http.StatusOK, result)
}

type httpStatusError struct {
	status int
	msg    string
}

func (e httpStatusError) Error() string { return e.msg }

func statusForError(err error) int {
	var statusErr httpStatusError
	if err != nil && errors.As(err, &statusErr) && statusErr.status > 0 {
		return statusErr.status
	}
	return http.StatusBadGateway
}

func collectEmails(req inviteRequest, maxEmails int) ([]string, error) {
	if maxEmails <= 0 {
		maxEmails = defaultMaxEmails
	}
	if maxEmails > upperMaxEmails {
		maxEmails = upperMaxEmails
	}

	seen := map[string]struct{}{}
	out := make([]string, 0)
	add := func(raw string) {
		for _, item := range splitEmailList(raw) {
			email := strings.TrimSpace(item)
			if email == "" {
				continue
			}
			key := strings.ToLower(email)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, email)
		}
	}
	for _, item := range req.Emails {
		add(item)
	}
	add(req.EmailsText)

	if len(out) == 0 {
		return nil, fmt.Errorf("at least one email is required")
	}
	if len(out) > maxEmails {
		return nil, fmt.Errorf("too many emails: got %d, max %d", len(out), maxEmails)
	}
	for _, email := range out {
		if !emailPattern.MatchString(email) {
			return nil, fmt.Errorf("invalid email address %q", email)
		}
	}
	return out, nil
}

func splitEmailList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
}

func selectAccount(accounts []accountInfo, req inviteRequest) (accountInfo, error) {
	authIndex := strings.TrimSpace(req.AuthIndex)
	authName := strings.TrimSpace(req.AuthName)
	if authIndex == "" && authName == "" {
		return accountInfo{}, fmt.Errorf("auth_index or auth_name is required")
	}
	for _, account := range accounts {
		if authIndex != "" && strings.EqualFold(account.AuthIndex, authIndex) {
			return account, nil
		}
		if authName != "" && account.Name == authName {
			return account, nil
		}
	}
	return accountInfo{}, fmt.Errorf("selected Codex credential was not found")
}

func fetchCodexAccounts(req pluginapi.ManagementRequest, explicitOrigin string) ([]accountInfo, error) {
	origin, errOrigin := resolveManagementOrigin(req, explicitOrigin)
	if errOrigin != nil {
		return nil, errOrigin
	}
	authHeader := strings.TrimSpace(req.Headers.Get("Authorization"))
	if authHeader == "" {
		return nil, httpStatusError{status: http.StatusUnauthorized, msg: "CPA management key is required"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, raw, errFetch := callLocalManagement(ctx, origin, http.MethodGet, authFilesPath, authHeader, nil)
	if errFetch != nil {
		return nil, errFetch
	}
	if status != http.StatusOK {
		return nil, httpStatusError{status: http.StatusBadGateway, msg: fmt.Sprintf("failed to list CPA auth files: status %d", status)}
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(raw, &payload); errDecode != nil {
		return nil, fmt.Errorf("decode auth files response: %w", errDecode)
	}

	accounts := make([]accountInfo, 0)
	for _, file := range payload.Files {
		provider := firstString(file, "provider", "type")
		if !strings.EqualFold(provider, "codex") {
			continue
		}
		if boolValue(file["disabled"]) || boolValue(file["unavailable"]) {
			continue
		}
		name := firstString(file, "name")
		if name == "" || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		accounts = append(accounts, accountInfo{
			AuthIndex:        firstString(file, "auth_index", "auth-index"),
			Name:             name,
			Label:            firstString(file, "label"),
			Email:            firstString(file, "email"),
			Account:          firstString(file, "account"),
			ChatGPTAccountID: nestedString(file, "id_token", "chatgpt_account_id"),
			Status:           firstString(file, "status"),
			Source:           firstString(file, "source"),
		})
	}
	sort.Slice(accounts, func(i, j int) bool {
		left := strings.ToLower(accounts[i].Email + accounts[i].Name)
		right := strings.ToLower(accounts[j].Email + accounts[j].Name)
		return left < right
	})
	return accounts, nil
}

func fetchCodexCredential(req pluginapi.ManagementRequest, explicitOrigin string, account accountInfo) (codexCredential, error) {
	origin, errOrigin := resolveManagementOrigin(req, explicitOrigin)
	if errOrigin != nil {
		return codexCredential{}, errOrigin
	}
	authHeader := strings.TrimSpace(req.Headers.Get("Authorization"))
	if authHeader == "" {
		return codexCredential{}, httpStatusError{status: http.StatusUnauthorized, msg: "CPA management key is required"}
	}

	path := authFileDownloadPath + "?name=" + url.QueryEscape(account.Name)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, raw, errFetch := callLocalManagement(ctx, origin, http.MethodGet, path, authHeader, nil)
	if errFetch != nil {
		return codexCredential{}, errFetch
	}
	if status != http.StatusOK {
		return codexCredential{}, httpStatusError{status: http.StatusBadGateway, msg: fmt.Sprintf("failed to download selected Codex credential: status %d", status)}
	}

	credential, errCredential := parseCodexCredential(raw)
	if errCredential != nil {
		return codexCredential{}, errCredential
	}
	if credential.AccessToken == "" {
		return codexCredential{}, httpStatusError{status: http.StatusBadRequest, msg: "selected Codex credential does not contain access_token"}
	}
	return credential, nil
}

func resolveManagementOrigin(req pluginapi.ManagementRequest, explicit string) (string, error) {
	for _, candidate := range []string{
		explicit,
		req.Headers.Get(requestManagementOrigin),
		req.Headers.Get("Origin"),
	} {
		origin, errOrigin := normalizeOrigin(candidate)
		if errOrigin == nil && origin != "" {
			return origin, nil
		}
	}
	return "", httpStatusError{status: http.StatusBadRequest, msg: "management origin is required"}
}

func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil {
		return "", errParse
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported origin scheme")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("origin host is required")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func callLocalManagement(ctx context.Context, origin, method, path, authorization string, body []byte) (int, []byte, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, errRequest := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(body))
	if errRequest != nil {
		return 0, nil, errRequest
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authorization)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		return 0, nil, errDo
	}
	defer func() { _ = resp.Body.Close() }()
	raw, errRead := readLimited(resp.Body, upstreamBodyLimit)
	if errRead != nil {
		return resp.StatusCode, nil, errRead
	}
	return resp.StatusCode, raw, nil
}

func parseCodexCredential(raw []byte) (codexCredential, error) {
	var data map[string]any
	if errUnmarshal := json.Unmarshal(raw, &data); errUnmarshal != nil {
		return codexCredential{}, fmt.Errorf("decode Codex credential: %w", errUnmarshal)
	}
	return codexCredential{
		AccessToken: firstNestedString(data,
			[]string{"access_token"},
			[]string{"token_data", "access_token"},
		),
		AccountID: firstNestedString(data,
			[]string{"account_id"},
			[]string{"chatgpt_account_id"},
			[]string{"token_data", "account_id"},
			[]string{"token_data", "chatgpt_account_id"},
		),
		Email: firstNestedString(data,
			[]string{"email"},
			[]string{"token_data", "email"},
		),
	}, nil
}

func sendInvite(ctx context.Context, cfg pluginConfig, credential codexCredential, account accountInfo, emails []string, referralKey string, requestCookie string, proxyURL string) (inviteResponse, error) {
	endpoint, errEndpoint := inviteEndpoint(cfg.BaseURL)
	if errEndpoint != nil {
		return inviteResponse{}, errEndpoint
	}
	body, errMarshal := json.Marshal(map[string]any{
		"referral_key": referralKey,
		"emails":       emails,
	})
	if errMarshal != nil {
		return inviteResponse{}, errMarshal
	}

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if errRequest != nil {
		return inviteResponse{}, errRequest
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Oai-Language", cfg.Language)
	req.Header.Set("Originator", cfg.Originator)
	req.Header.Set("User-Agent", cfg.UserAgent)
	if credential.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", credential.AccountID)
	}
	if cookie := strings.TrimSpace(requestCookie); cookie != "" {
		req.Header.Set("Cookie", cookie)
	} else if cfg.Cookie != "" {
		req.Header.Set("Cookie", cfg.Cookie)
	}

	client, errClient := inviteHTTPClient(proxyURL)
	if errClient != nil {
		return inviteResponse{}, errClient
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return inviteResponse{}, errDo
	}
	defer func() { _ = resp.Body.Close() }()
	raw, errRead := readLimited(resp.Body, upstreamBodyLimit)
	if errRead != nil {
		return inviteResponse{}, errRead
	}

	result := inviteResponse{
		OK:          resp.StatusCode >= 200 && resp.StatusCode < 300,
		StatusCode:  resp.StatusCode,
		RequestID:   resp.Header.Get("x-oai-request-id"),
		Account:     account,
		Emails:      emails,
		ReferralKey: referralKey,
		Invites:     extractInviteLinks(raw),
	}
	var upstream any
	if len(raw) > 0 && json.Unmarshal(raw, &upstream) == nil {
		result.Upstream = upstream
	} else {
		result.UpstreamRaw = string(raw)
	}
	return result, nil
}

func inviteHTTPClient(proxyURL string) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return http.DefaultClient, nil
	}
	parsed, errParse := url.Parse(proxyURL)
	if errParse != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", errParse)
	}
	if parsed.Scheme == "" {
		return nil, fmt.Errorf("proxy URL scheme is required")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported proxy URL scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("proxy URL host is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(parsed)
	return &http.Client{Transport: transport}, nil
}

func inviteEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, errParse := url.Parse(baseURL)
	if errParse != nil {
		return "", errParse
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported ChatGPT base URL scheme")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("ChatGPT base URL host is required")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + inviteEndpointPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func extractInviteLinks(raw []byte) []inviteLink {
	var parsed struct {
		Invites []inviteLink `json:"invites"`
	}
	if errUnmarshal := json.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return nil
	}
	return parsed.Invites
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	data, errRead := io.ReadAll(limited)
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response body is too large")
	}
	return data, nil
}

func firstString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(data[key]); value != "" {
			return value
		}
	}
	return ""
}

func nestedString(data map[string]any, path ...string) string {
	var current any = data
	for _, key := range path {
		next, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = next[key]
	}
	return stringValue(current)
}

func firstNestedString(data map[string]any, paths ...[]string) string {
	for _, path := range paths {
		if value := nestedString(data, path...); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func boolValue(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func jsonResponse(status int, body any) pluginapi.ManagementResponse {
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		status = http.StatusInternalServerError
		raw = []byte(`{"error":"failed to encode response"}`)
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{contentTypeJSON}},
		Body:       raw,
	}
}

func htmlResponse(status int, body string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{contentTypeHTML}},
		Body:       []byte(body),
	}
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func renderInvitePage(cfg pluginConfig) string {
	defaults := map[string]any{
		"referralKey": cfg.ReferralKey,
		"baseURL":     cfg.BaseURL,
		"language":    cfg.Language,
		"originator":  cfg.Originator,
		"userAgent":   cfg.UserAgent,
		"maxEmails":   cfg.MaxEmailsPerRequest,
	}
	rawDefaults, errMarshal := json.Marshal(defaults)
	if errMarshal != nil {
		rawDefaults = []byte(`{"referralKey":"codex_referral_persistent_invite","baseURL":"https://chatgpt.com","language":"zh-CN","originator":"Codex Desktop","userAgent":"","maxEmails":10}`)
	}
	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Codex Invite</title>
  <style>
    :root {
      color-scheme: light dark;
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: Canvas;
      color: CanvasText;
      letter-spacing: 0;
    }
    * { box-sizing: border-box; }
    body { margin: 0; background: Canvas; color: CanvasText; }
    main { max-width: 1120px; margin: 0 auto; padding: 24px; }
    header { display: flex; align-items: end; justify-content: space-between; gap: 16px; margin-bottom: 18px; }
    h1 { margin: 0; font-size: 24px; font-weight: 760; letter-spacing: 0; }
    h2 { margin: 0 0 14px; font-size: 15px; font-weight: 720; letter-spacing: 0; }
    label { display: grid; gap: 7px; font-size: 13px; font-weight: 650; min-width: 0; }
    input, select, textarea, button { font: inherit; }
    input, select, textarea {
      width: 100%;
      border: 1px solid color-mix(in srgb, CanvasText 18%, Canvas 82%);
      border-radius: 6px;
      padding: 9px 10px;
      background: Canvas;
      color: CanvasText;
    }
    textarea { min-height: 116px; resize: vertical; line-height: 1.45; }
    button {
      border: 0;
      border-radius: 6px;
      padding: 9px 12px;
      background: #0f766e;
      color: #fff;
      font-weight: 720;
      cursor: pointer;
      white-space: nowrap;
    }
    button.secondary { background: color-mix(in srgb, CanvasText 10%, Canvas 90%); color: CanvasText; }
    button.warning { background: #b45309; }
    button:disabled { opacity: .54; cursor: not-allowed; }
    .header-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: end; gap: 10px; }
    .locale-control { display: flex; align-items: center; gap: 8px; min-width: auto; font-size: 12px; font-weight: 650; }
    .locale-control select { width: auto; min-width: 120px; padding: 7px 9px; }
    .layout { display: grid; grid-template-columns: 340px minmax(0, 1fr); gap: 16px; align-items: start; }
    .stack { display: grid; gap: 16px; }
    .panel {
      border: 1px solid color-mix(in srgb, CanvasText 14%, Canvas 86%);
      border-radius: 8px;
      padding: 16px;
      background: color-mix(in srgb, Canvas 96%, CanvasText 4%);
    }
    .collapsible { padding: 0; overflow: hidden; }
    .collapsible > summary {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      padding: 16px;
      cursor: pointer;
      list-style: none;
    }
    .collapsible > summary::-webkit-details-marker { display: none; }
    .collapsible > summary::after {
      content: "+";
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 22px;
      height: 22px;
      border-radius: 6px;
      background: color-mix(in srgb, CanvasText 10%, Canvas 90%);
      font-size: 15px;
      font-weight: 760;
      flex: 0 0 auto;
    }
    .collapsible[open] > summary {
      border-bottom: 1px solid color-mix(in srgb, CanvasText 12%, Canvas 88%);
    }
    .collapsible[open] > summary::after { content: "-"; }
    .collapsible-body { padding: 16px; }
    .summary-text { display: grid; gap: 3px; min-width: 0; }
    .summary-title { font-size: 15px; font-weight: 720; letter-spacing: 0; }
    .summary-subtitle {
      color: color-mix(in srgb, CanvasText 62%, Canvas 38%);
      font-size: 12px;
      font-weight: 520;
      line-height: 1.35;
    }
    .fields { display: grid; gap: 13px; }
    .grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 13px; }
    .actions { display: flex; flex-wrap: wrap; gap: 9px; align-items: center; }
    .actions button { width: auto; }
    .inline { display: flex; gap: 9px; align-items: center; }
    .inline input[type="checkbox"] { width: auto; margin: 0; }
    .metric {
      min-height: 34px;
      display: inline-flex;
      align-items: center;
      border-radius: 6px;
      padding: 6px 9px;
      font-size: 12px;
      font-weight: 700;
      background: color-mix(in srgb, #2563eb 12%, Canvas 88%);
      color: color-mix(in srgb, #2563eb 72%, CanvasText 28%);
    }
    .muted { color: color-mix(in srgb, CanvasText 62%, Canvas 38%); font-size: 12px; font-weight: 520; }
    .status {
      margin-top: 16px;
      white-space: pre-wrap;
      word-break: break-word;
      border-radius: 8px;
      padding: 13px;
      background: color-mix(in srgb, #2563eb 10%, Canvas 90%);
      border: 1px solid color-mix(in srgb, #2563eb 18%, Canvas 82%);
      font-size: 13px;
      line-height: 1.45;
    }
    .status.error {
      background: color-mix(in srgb, #dc2626 12%, Canvas 88%);
      border-color: color-mix(in srgb, #dc2626 24%, Canvas 76%);
    }
    .links { display: grid; gap: 8px; margin-top: 12px; }
    .links a {
      color: #0f766e;
      overflow-wrap: anywhere;
      border: 1px solid color-mix(in srgb, CanvasText 12%, Canvas 88%);
      border-radius: 6px;
      padding: 9px 10px;
      background: Canvas;
      text-decoration: none;
    }
    @media (max-width: 860px) {
      main { padding: 16px; }
      header { display: grid; align-items: start; }
      .header-actions { justify-content: start; }
      .layout, .grid { grid-template-columns: 1fr; }
      .actions, .inline { display: grid; }
      .actions button { width: 100%; }
    }
  </style>
</head>
<body>
  <main>
    <header>
      <h1 data-i18n="app.title">Codex Invite</h1>
      <div class="header-actions">
        <label class="locale-control">
          <span data-i18n="app.language">Language</span>
          <select id="localeSelect" autocomplete="off">
            <option value="en">English</option>
            <option value="zh-CN">中文</option>
          </select>
        </label>
        <span class="metric" id="emailCount">0 emails</span>
      </div>
    </header>
    <div class="layout">
      <div class="stack">
        <section class="panel">
          <h2 data-i18n="connection.title">Connection</h2>
          <div class="fields">
            <label><span data-i18n="connection.managementKey">CPA management key</span>
              <input id="managementKey" type="password" autocomplete="off" spellcheck="false">
            </label>
            <div class="actions">
              <button id="loadAccounts" type="button" data-i18n="connection.loadAccounts">Load accounts</button>
            </div>
            <label><span data-i18n="connection.account">Codex account</span>
              <select id="account"></select>
            </label>
            <span id="accountCount" class="muted"></span>
          </div>
        </section>
        <details class="panel collapsible" id="settingsPanel">
          <summary>
            <span class="summary-text">
              <span class="summary-title" data-i18n="settings.title">Settings</span>
              <span class="summary-subtitle" data-i18n="settings.summary">Defaults work for most cases</span>
            </span>
          </summary>
          <div class="fields collapsible-body">
            <label><span data-i18n="settings.referralKey">Referral key</span>
              <input id="referralKey" spellcheck="false">
            </label>
            <label><span data-i18n="settings.baseUrl">ChatGPT base URL</span>
              <input id="baseUrl" spellcheck="false">
            </label>
            <div class="grid">
              <label><span data-i18n="settings.upstreamLanguage">Language</span>
                <input id="language" spellcheck="false">
              </label>
              <label><span data-i18n="settings.originator">Originator</span>
                <input id="originator" spellcheck="false">
              </label>
            </div>
            <label><span data-i18n="settings.userAgent">User-Agent</span>
              <input id="userAgent" spellcheck="false">
            </label>
            <label><span data-i18n="settings.maxEmails">Max emails per request</span>
              <input id="maxEmails" type="number" min="1" max="50" step="1">
            </label>
            <label><span data-i18n="settings.cookie">Cookie</span>
              <textarea id="cookie" autocomplete="off" spellcheck="false"></textarea>
            </label>
            <div class="actions">
              <button id="saveLocal" type="button" class="secondary" data-i18n="settings.saveLocal">Save local</button>
              <button id="resetLocal" type="button" class="secondary" data-i18n="settings.resetLocal">Reset local</button>
            </div>
          </div>
        </details>
      </div>
      <section class="panel">
        <h2 data-i18n="invite.title">Invite</h2>
        <div class="fields">
          <label><span data-i18n="invite.proxyUrl">Proxy URL</span>
            <input id="proxyUrl" spellcheck="false" placeholder="http://127.0.0.1:7890">
          </label>
          <label><span data-i18n="invite.emails">Email addresses</span>
            <textarea id="emails" spellcheck="false" data-i18n-placeholder="invite.emailsPlaceholder" placeholder="name@example.com&#10;teammate@example.com"></textarea>
          </label>
          <div class="actions">
            <button id="send" type="button" data-i18n="invite.send">Send invites</button>
            <button id="clearResult" type="button" class="secondary" data-i18n="invite.clearResult">Clear result</button>
          </div>
        </div>
      </section>
    </div>
    <section id="status" class="status" hidden></section>
    <section id="links" class="links"></section>
  </main>
  <script>
    const DEFAULTS = ` + string(rawDefaults) + `;
    const STORAGE_KEY = 'codex-invite-settings-v2';
    const LOCALE_STORAGE_KEY = 'codex-invite-locale-v1';
    const TRANSLATIONS = {
      en: {
        'app.title': 'Codex Invite',
        'app.language': 'Language',
        'connection.title': 'Connection',
        'connection.managementKey': 'CPA management key',
        'connection.loadAccounts': 'Load accounts',
        'connection.account': 'Codex account',
        'settings.title': 'Settings',
        'settings.summary': 'Defaults work for most cases',
        'settings.referralKey': 'Referral key',
        'settings.baseUrl': 'ChatGPT base URL',
        'settings.upstreamLanguage': 'Language',
        'settings.originator': 'Originator',
        'settings.userAgent': 'User-Agent',
        'settings.maxEmails': 'Max emails per request',
        'settings.cookie': 'Cookie',
        'settings.saveLocal': 'Save local',
        'settings.resetLocal': 'Reset local',
        'invite.title': 'Invite',
        'invite.proxyUrl': 'Proxy URL',
        'invite.emails': 'Email addresses',
        'invite.emailsPlaceholder': 'name@example.com\nteammate@example.com',
        'invite.send': 'Send invites',
        'invite.clearResult': 'Clear result',
        'email.countOne': '{count} email',
        'email.countOther': '{count} emails',
        'account.none': 'No Codex accounts loaded',
        'account.count': '{count} accounts loaded',
        'status.localLoadFailed': 'Failed to load local settings: {error}',
        'status.localSaved': 'Local settings saved.',
        'status.localReset': 'Local settings reset.',
        'status.accountsLoaded': 'Accounts loaded.',
        'error.managementKeyRequired': 'CPA management key is required',
        'error.loadAccountsFailed': 'Failed to load accounts',
        'error.selectAccount': 'Select a Codex account',
        'error.inviteFailed': 'Invite request failed'
      },
      'zh-CN': {
        'app.title': 'Codex 邀请',
        'app.language': '界面语言',
        'connection.title': '连接',
        'connection.managementKey': 'CPA 管理密钥',
        'connection.loadAccounts': '加载账号',
        'connection.account': 'Codex 账号',
        'settings.title': '设置',
        'settings.summary': '默认值通常可以直接使用',
        'settings.referralKey': '邀请 referral key',
        'settings.baseUrl': 'ChatGPT 基础地址',
        'settings.upstreamLanguage': '上游语言',
        'settings.originator': 'Originator',
        'settings.userAgent': 'User-Agent',
        'settings.maxEmails': '单次最多邮箱数',
        'settings.cookie': 'Cookie',
        'settings.saveLocal': '保存到本地',
        'settings.resetLocal': '恢复默认',
        'invite.title': '邀请',
        'invite.proxyUrl': '代理地址',
        'invite.emails': '邮箱地址',
        'invite.emailsPlaceholder': 'name@example.com\nteammate@example.com',
        'invite.send': '发送邀请',
        'invite.clearResult': '清空结果',
        'email.countOne': '{count} 个邮箱',
        'email.countOther': '{count} 个邮箱',
        'account.none': '未加载 Codex 账号',
        'account.count': '已加载 {count} 个账号',
        'status.localLoadFailed': '加载本地设置失败：{error}',
        'status.localSaved': '本地设置已保存。',
        'status.localReset': '本地设置已恢复默认。',
        'status.accountsLoaded': '账号已加载。',
        'error.managementKeyRequired': '需要填写 CPA 管理密钥',
        'error.loadAccountsFailed': '加载账号失败',
        'error.selectAccount': '请选择 Codex 账号',
        'error.inviteFailed': '邀请请求失败'
      }
    };
    const origin = window.location.origin;

    function normalizeLocale(raw) {
      return String(raw || '').toLowerCase().startsWith('zh') ? 'zh-CN' : 'en';
    }

    function detectLocale() {
      try {
        const saved = window.localStorage.getItem(LOCALE_STORAGE_KEY);
        if (saved) return normalizeLocale(saved);
      } catch (error) {
        // Ignore storage access failures and fall back to the browser locale.
      }
      const candidates = navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language];
      for (const item of candidates) {
        if (String(item || '').toLowerCase().startsWith('zh')) return 'zh-CN';
      }
      return 'en';
    }

    const state = { accounts: [], locale: detectLocale() };

    function field(id) {
      return document.getElementById(id);
    }

    const accountSelect = field('account');
    const statusBox = field('status');
    const linksBox = field('links');
    const keyInput = field('managementKey');
    const localeSelect = field('localeSelect');
    const loadButton = field('loadAccounts');
    const saveLocalButton = field('saveLocal');
    const resetLocalButton = field('resetLocal');
    const sendButton = field('send');
    const clearResultButton = field('clearResult');
    const accountCount = field('accountCount');
    const emailCount = field('emailCount');

    function t(key, params) {
      const dictionary = TRANSLATIONS[state.locale] || TRANSLATIONS.en;
      let message = dictionary[key] || TRANSLATIONS.en[key] || key;
      for (const name of Object.keys(params || {})) {
        message = message.split('{' + name + '}').join(String(params[name]));
      }
      return message;
    }

    function emailCountText(count) {
      return t(count === 1 ? 'email.countOne' : 'email.countOther', { count });
    }

    function updateAccountCount() {
      accountCount.textContent = state.accounts.length ? t('account.count', { count: state.accounts.length }) : t('account.none');
    }

    function applyLocale() {
      document.documentElement.lang = state.locale;
      document.title = t('app.title');
      localeSelect.value = state.locale;
      for (const item of document.querySelectorAll('[data-i18n]')) {
        item.textContent = t(item.dataset.i18n);
      }
      for (const item of document.querySelectorAll('[data-i18n-placeholder]')) {
        item.placeholder = t(item.dataset.i18nPlaceholder);
      }
      updateAccountCount();
      updateEmailCount();
    }

    function changeLocale(locale) {
      state.locale = normalizeLocale(locale);
      try {
        window.localStorage.setItem(LOCALE_STORAGE_KEY, state.locale);
      } catch (error) {
        // The page remains usable if localStorage is unavailable.
      }
      applyLocale();
    }

    function setStatus(message, error) {
      statusBox.hidden = false;
      statusBox.textContent = message;
      statusBox.className = 'status' + (error ? ' error' : '');
    }

    function clearResult() {
      statusBox.hidden = true;
      statusBox.textContent = '';
      linksBox.innerHTML = '';
    }

    function formatError(data, fallback) {
      if (!data) return fallback;
      if (typeof data === 'string') return data;
      return data.message || data.error || fallback;
    }

    async function readJSON(response) {
      const text = await response.text();
      if (!text) return {};
      try {
        return JSON.parse(text);
      } catch (error) {
        return { error: text };
      }
    }

    function authHeaders() {
      const key = keyInput.value.trim();
      if (!key) throw new Error(t('error.managementKeyRequired'));
      const authorization = key.toLowerCase().startsWith('bearer ') ? key : 'Bearer ' + key;
      return {
        'Authorization': authorization,
        'X-Codex-Invite-Origin': origin
      };
    }

    function numericMaxEmails() {
      const value = Number.parseInt(field('maxEmails').value, 10);
      if (!Number.isFinite(value) || value < 1) return DEFAULTS.maxEmails || 10;
      return Math.min(value, 50);
    }

    function getSettings() {
      return {
        referral_key: field('referralKey').value.trim(),
        base_url: field('baseUrl').value.trim(),
        proxy_url: field('proxyUrl').value.trim(),
        language: field('language').value.trim(),
        originator: field('originator').value.trim(),
        user_agent: field('userAgent').value.trim(),
        max_emails_per_request: numericMaxEmails()
      };
    }

    function settingsForStorage() {
      const settings = getSettings();
      return {
        referralKey: settings.referral_key,
        baseURL: settings.base_url,
        language: settings.language,
        originator: settings.originator,
        userAgent: settings.user_agent,
        maxEmails: settings.max_emails_per_request
      };
    }

    function applySettings(raw) {
      const data = raw || {};
      field('referralKey').value = data.referral_key || data.referralKey || DEFAULTS.referralKey || '';
      field('baseUrl').value = data.base_url || data.baseURL || DEFAULTS.baseURL || 'https://chatgpt.com';
      field('proxyUrl').value = '';
      field('language').value = data.language || DEFAULTS.language || 'zh-CN';
      field('originator').value = data.originator || DEFAULTS.originator || 'Codex Desktop';
      field('userAgent').value = data.user_agent || data.userAgent || DEFAULTS.userAgent || '';
      field('maxEmails').value = data.max_emails_per_request || data.maxEmails || DEFAULTS.maxEmails || 10;
    }

    function loadLocalSettings() {
      try {
        const raw = window.localStorage.getItem(STORAGE_KEY);
        if (raw) {
          applySettings({ ...DEFAULTS, ...JSON.parse(raw) });
          return;
        }
      } catch (error) {
        setStatus(t('status.localLoadFailed', { error: error.message || String(error) }), true);
      }
      applySettings(DEFAULTS);
    }

    function saveLocalSettings() {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify(settingsForStorage()));
      setStatus(t('status.localSaved'));
    }

    function resetLocalSettings() {
      window.localStorage.removeItem(STORAGE_KEY);
      applySettings(DEFAULTS);
      setStatus(t('status.localReset'));
      updateEmailCount();
    }

    function splitEmails(text) {
      return text.split(/[,\s;]+/).map((item) => item.trim()).filter(Boolean);
    }

    function updateEmailCount() {
      const count = splitEmails(field('emails').value).length;
      emailCount.textContent = emailCountText(count);
      sendButton.disabled = count === 0 || !accountSelect.selectedOptions.length;
    }

    function renderAccounts(accounts) {
      accountSelect.innerHTML = '';
      state.accounts = Array.isArray(accounts) ? accounts : [];
      for (const account of state.accounts) {
        const option = document.createElement('option');
        option.value = account.auth_index || account.name;
        option.dataset.name = account.name;
        option.textContent = [account.email, account.account, account.name].filter(Boolean).join(' - ') || account.name;
        accountSelect.appendChild(option);
      }
      updateAccountCount();
      updateEmailCount();
    }

    async function loadAccounts() {
      clearResult();
      loadButton.disabled = true;
      try {
        const response = await fetch('/v0/management/codex-invite/accounts', { headers: authHeaders() });
        const data = await readJSON(response);
        if (!response.ok) throw new Error(formatError(data, t('error.loadAccountsFailed')));
        renderAccounts(data.accounts || []);
        setStatus(t('status.accountsLoaded'));
      } catch (error) {
        setStatus(error.message || String(error), true);
      } finally {
        loadButton.disabled = false;
      }
    }

    async function sendInvites() {
      clearResult();
      sendButton.disabled = true;
      try {
        const selected = accountSelect.selectedOptions[0];
        if (!selected) throw new Error(t('error.selectAccount'));
        const settings = getSettings();
        const payload = {
          auth_index: selected.value,
          auth_name: selected.dataset.name || '',
          emails_text: field('emails').value,
          referral_key: settings.referral_key,
          base_url: settings.base_url,
          proxy_url: settings.proxy_url,
          language: settings.language,
          originator: settings.originator,
          user_agent: settings.user_agent,
          max_emails_per_request: settings.max_emails_per_request,
          cookie: field('cookie').value,
          management_origin: origin
        };
        const response = await fetch('/v0/management/codex-invite/invite', {
          method: 'POST',
          headers: { ...authHeaders(), 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        const data = await readJSON(response);
        if (!response.ok) throw new Error(formatError(data, t('error.inviteFailed')));
        const ok = data.ok === true;
        setStatus(JSON.stringify(data, null, 2), !ok);
        for (const invite of data.invites || []) {
          if (!invite.invite_url) continue;
          const link = document.createElement('a');
          link.href = invite.invite_url;
          link.target = '_blank';
          link.rel = 'noreferrer';
          link.textContent = (invite.email || 'invite') + ': ' + invite.invite_url;
          linksBox.appendChild(link);
        }
      } catch (error) {
        setStatus(error.message || String(error), true);
      } finally {
        updateEmailCount();
      }
    }

    localeSelect.addEventListener('change', () => changeLocale(localeSelect.value));
    loadButton.addEventListener('click', loadAccounts);
    saveLocalButton.addEventListener('click', saveLocalSettings);
    resetLocalButton.addEventListener('click', resetLocalSettings);
    sendButton.addEventListener('click', sendInvites);
    clearResultButton.addEventListener('click', clearResult);
    field('emails').addEventListener('input', updateEmailCount);
    accountSelect.addEventListener('change', updateEmailCount);
    renderAccounts([]);
    applyLocale();
    loadLocalSettings();
    updateEmailCount();
  </script>
</body>
</html>`
}
