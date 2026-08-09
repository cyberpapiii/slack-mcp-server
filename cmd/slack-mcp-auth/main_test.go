package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAuthorizationURLUsesUserScopesAndPKCE(t *testing.T) {
	authorization, err := provider.NewPKCEAuthorization(defaultRedirectURI)
	require.NoError(t, err)

	raw, err := buildAuthorizationURL("client-id", "T1", []string{"channels:read", "chat:write"}, authorization)
	require.NoError(t, err)
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	query := parsed.Query()
	assert.Equal(t, "https", parsed.Scheme)
	assert.Equal(t, "slack.com", parsed.Host)
	assert.Equal(t, "/oauth/v2/authorize", parsed.Path)
	assert.Equal(t, "client-id", query.Get("client_id"))
	assert.Equal(t, "T1", query.Get("team"))
	assert.Equal(t, "channels:read,chat:write", query.Get("user_scope"))
	assert.Empty(t, query.Get("scope"))
	assert.Equal(t, authorization.State, query.Get("state"))
	assert.Equal(t, authorization.Challenge, query.Get("code_challenge"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.Equal(t, defaultRedirectURI, query.Get("redirect_uri"))
}

func TestCustomOAuthScopesContainLocalPowerUserScopes(t *testing.T) {
	scopes, err := customOAuthScopes("legacy-full")
	require.NoError(t, err)
	for _, scope := range []string{
		"channels:history", "channels:read", "channels:write", "chat:write",
		"dnd:read", "dnd:write", "files:read", "groups:history", "groups:read",
		"groups:write", "im:history", "im:read", "im:write", "lists:read",
		"lists:write", "mpim:history", "mpim:read", "mpim:write", "reactions:read",
		"reactions:write", "search:read", "stars:read", "usergroups:read",
		"usergroups:write", "users:read",
	} {
		assert.Contains(t, scopes, scope)
	}
	assert.NotContains(t, scopes, "channels:join")
	assert.NotContains(t, scopes, "channels:manage")
}

func TestDailyPowerOAuthScopesExcludeHiddenWrites(t *testing.T) {
	scopes, err := customOAuthScopes("daily-power")
	require.NoError(t, err)
	assert.Contains(t, scopes, "channels:history")
	assert.Contains(t, scopes, "channels:read")
	assert.Contains(t, scopes, "dnd:read")
	assert.Contains(t, scopes, "groups:read")
	assert.Contains(t, scopes, "users:read")
	assert.Contains(t, scopes, "lists:read")
	assert.NotContains(t, scopes, "chat:write")
	assert.NotContains(t, scopes, "dnd:write")
	assert.NotContains(t, scopes, "lists:write")
}

func TestLoginRequiresExpectedIdentity(t *testing.T) {
	t.Setenv("SLACK_MCP_OAUTH_CLIENT_ID", "client-id")
	err := runLogin([]string{"--no-open"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team and user IDs")
}

func TestCallbackRejectsWrongStateWithoutConsumingLogin(t *testing.T) {
	results := make(chan callbackResult, 1)
	server := callbackServer(results, "expected")
	request := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=secret&state=wrong", nil)
	response := httptest.NewRecorder()

	server.Handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	assert.Empty(t, results)
}

func TestCallbackAcceptsOnlyOneValidRequest(t *testing.T) {
	results := make(chan callbackResult, 1)
	server := callbackServer(results, "expected")
	firstDone := make(chan struct{})
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=first&state=expected", nil)
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		assert.Equal(t, http.StatusOK, response.Code)
		close(firstDone)
	}()
	result := <-results

	request := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=second&state=expected", nil)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusConflict, response.Code)
	result.done <- nil

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-firstDone:
	case <-ctx.Done():
		t.Fatal("first callback did not finish")
	}
}
