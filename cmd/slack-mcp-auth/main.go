package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
)

const (
	defaultAccount     = "slack-mcp-local"
	defaultRedirectURI = "http://127.0.0.1:19453/oauth/callback"
	loginTimeout       = 10 * time.Minute
)

var openURL = func(target string) error {
	return exec.Command("open", target).Start()
}

type loginOptions struct {
	clientID     string
	clientSecret string
	account      string
	redirectURI  string
	teamID       string
	userID       string
	preset       string
	replace      bool
	noOpen       bool
}

type callbackResult struct {
	code  string
	state string
	err   string
	done  chan error
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "login":
		err = runLogin(os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "logout":
		err = runLogout(os.Args[2:])
	case "manifest":
		err = runManifest(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: slack-mcp-auth <login|status|logout|manifest> [options]")
}

func runLogin(args []string) error {
	options, err := parseLoginOptions(args)
	if err != nil {
		return err
	}
	if options.clientID == "" {
		return errors.New("Slack OAuth client ID is required; set SLACK_MCP_OAUTH_CLIENT_ID or use --client-id")
	}
	if options.teamID == "" || options.userID == "" {
		return errors.New("expected Slack team and user IDs are required; use --team and --user")
	}

	authorization, err := provider.NewPKCEAuthorization(options.redirectURI)
	if err != nil {
		return err
	}
	listener, err := listenForCallback(options.redirectURI)
	if err != nil {
		return err
	}
	defer listener.Close()

	scopes, err := customOAuthScopes(options.preset)
	if err != nil {
		return err
	}
	authorizeURL, err := buildAuthorizationURL(options.clientID, options.teamID, scopes, authorization)
	if err != nil {
		return err
	}

	callback := make(chan callbackResult, 1)
	server := callbackServer(callback, authorization.State)
	serveErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
	}()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()

	fmt.Println("Slack permission page:")
	fmt.Println(authorizeURL)
	if !options.noOpen {
		if err := openURL(authorizeURL); err != nil {
			fmt.Fprintln(os.Stderr, "Browser did not open automatically. Open URL above.")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()
	select {
	case result := <-callback:
		if result.err != "" {
			err := errors.New("Slack authorization denied: " + result.err)
			result.done <- err
			return err
		}
		record, err := provider.ExchangeOAuthAuthorizationCode(
			ctx, http.DefaultClient, options.clientID, options.clientSecret, result.code,
			result.state, options.redirectURI, authorization, time.Now().UTC(),
		)
		if err == nil {
			err = saveAuthorizedCredential(ctx, options, scopes, record)
		}
		result.done <- err
		if err != nil {
			return err
		}
		fmt.Printf("Connected. Team %s. User %s. Keychain account %s.\n", record.TeamID, record.UserID, options.account)
		return nil
	case err := <-serveErrors:
		return fmt.Errorf("OAuth callback server: %w", err)
	case <-ctx.Done():
		return errors.New("Slack authorization timed out after 10 minutes")
	}
}

func parseLoginOptions(args []string) (loginOptions, error) {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	options := loginOptions{}
	flags.StringVar(&options.clientID, "client-id", os.Getenv("SLACK_MCP_OAUTH_CLIENT_ID"), "Slack app client ID")
	options.clientSecret = os.Getenv("SLACK_MCP_OAUTH_CLIENT_SECRET")
	flags.StringVar(&options.account, "account", envOrDefault("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", defaultAccount), "macOS Keychain account")
	options.redirectURI = defaultRedirectURI
	flags.StringVar(&options.teamID, "team", os.Getenv("SLACK_MCP_OAUTH_TEAM_ID"), "expected Slack team ID")
	flags.StringVar(&options.userID, "user", os.Getenv("SLACK_MCP_OAUTH_USER_ID"), "expected Slack user ID")
	flags.StringVar(&options.preset, "preset", "daily-power", "scope preset: daily-power or legacy-full")
	flags.BoolVar(&options.replace, "replace", false, "replace existing credential after exact identity validation")
	flags.BoolVar(&options.noOpen, "no-open", false, "print authorization URL without opening browser")
	if err := flags.Parse(args); err != nil {
		return loginOptions{}, err
	}
	return options, nil
}

func buildAuthorizationURL(clientID, teamID string, scopes []string, authorization *provider.PKCEAuthorization) (string, error) {
	if authorization == nil {
		return "", errors.New("PKCE authorization is required")
	}
	target, err := url.Parse("https://slack.com/oauth/v2/authorize")
	if err != nil {
		return "", err
	}
	query := target.Query()
	query.Set("client_id", clientID)
	query.Set("user_scope", strings.Join(scopes, ","))
	query.Set("redirect_uri", authorization.RedirectURI)
	query.Set("state", authorization.State)
	query.Set("code_challenge", authorization.Challenge)
	query.Set("code_challenge_method", "S256")
	if teamID != "" {
		query.Set("team", teamID)
	}
	target.RawQuery = query.Encode()
	return target.String(), nil
}

func listenForCallback(redirectURI string) (net.Listener, error) {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", parsed.Host)
	if err != nil {
		return nil, fmt.Errorf("listen for OAuth callback on %s: %w", parsed.Host, err)
	}
	return listener, nil
}

func callbackServer(results chan<- callbackResult, expectedState string) *http.Server {
	mux := http.NewServeMux()
	var accepted atomic.Bool
	mux.HandleFunc("/oauth/callback", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if request.Method != http.MethodGet {
			http.Error(response, "Method not allowed.", http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Query().Get("state") != expectedState {
			http.Error(response, "Invalid OAuth state.", http.StatusBadRequest)
			return
		}
		if !accepted.CompareAndSwap(false, true) {
			http.Error(response, "OAuth callback already received.", http.StatusConflict)
			return
		}
		result := callbackResult{
			code: request.URL.Query().Get("code"), state: request.URL.Query().Get("state"),
			err: request.URL.Query().Get("error"), done: make(chan error, 1),
		}
		select {
		case results <- result:
		case <-request.Context().Done():
			return
		}
		select {
		case err := <-result.done:
			if err != nil {
				http.Error(response, "Slack MCP login failed. Return to Terminal.", http.StatusBadRequest)
				return
			}
		case <-request.Context().Done():
			return
		case <-time.After(loginTimeout):
			http.Error(response, "Slack MCP login timed out. Return to Terminal.", http.StatusGatewayTimeout)
			return
		}
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(response, "Slack MCP connected. You may close this tab.")
	})
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}

func saveAuthorizedCredential(ctx context.Context, options loginOptions, scopes []string, record provider.OAuthTokenRecord) error {
	return provider.WithOAuthCredentialLock(ctx, func() error {
		store, err := provider.NewKeychainCredentialStore(options.account)
		if err != nil {
			return err
		}
		expectedGeneration := uint64(0)
		current, loadErr := store.Load(ctx)
		if loadErr == nil {
			if !options.replace {
				return errors.New("OAuth credential already exists; use --replace to reauthorize")
			}
			expectedGeneration = current.Generation
		} else if !errors.Is(loadErr, provider.ErrCredentialNotFound) {
			return loadErr
		}
		record.Generation = expectedGeneration + 1
		return provider.CommitAuthorizedOAuthCredential(ctx, store, expectedGeneration, record, scopes,
			func(ctx context.Context, candidate provider.OAuthTokenRecord) error {
				identity, err := slack.New(candidate.AccessToken).AuthTestContext(ctx)
				if err != nil {
					return err
				}
				if identity.TeamID != options.teamID || identity.UserID != options.userID {
					return fmt.Errorf("expected team %s user %s; received team %s user %s", options.teamID, options.userID, identity.TeamID, identity.UserID)
				}
				if candidate.TeamID != identity.TeamID || candidate.UserID != identity.UserID {
					return errors.New("OAuth response identity does not match auth.test")
				}
				return nil
			})
	})
}

func runStatus(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	account := flags.String("account", envOrDefault("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", defaultAccount), "macOS Keychain account")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := provider.NewKeychainCredentialStore(*account)
	if err != nil {
		return err
	}
	record, err := store.Load(context.Background())
	if err != nil {
		return err
	}
	status := struct {
		Account    string    `json:"account"`
		TeamID     string    `json:"team_id"`
		UserID     string    `json:"user_id"`
		ExpiresAt  time.Time `json:"expires_at"`
		Generation uint64    `json:"generation"`
		Scopes     []string  `json:"scopes"`
	}{*account, record.TeamID, record.UserID, record.ExpiresAt, record.Generation, record.Scopes}
	encoded, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func runLogout(args []string) error {
	flags := flag.NewFlagSet("logout", flag.ContinueOnError)
	account := flags.String("account", envOrDefault("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", defaultAccount), "macOS Keychain account")
	yes := flags.Bool("yes", false, "delete credential without another prompt")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*yes {
		return errors.New("logout deletes the local OAuth credential; rerun with --yes")
	}
	store, err := provider.NewKeychainCredentialStore(*account)
	if err != nil {
		return err
	}
	if err := provider.WithOAuthCredentialLock(context.Background(), func() error { return store.Delete(context.Background()) }); err != nil {
		return err
	}
	fmt.Println("Local OAuth credential deleted. Slack app authorization remains installed until revoked in Slack.")
	return nil
}

func runManifest(args []string) error {
	flags := flag.NewFlagSet("manifest", flag.ContinueOnError)
	preset := flags.String("preset", "daily-power", "scope preset: daily-power or legacy-full")
	if err := flags.Parse(args); err != nil {
		return err
	}
	scopes, err := customOAuthScopes(*preset)
	if err != nil {
		return err
	}
	manifest := map[string]any{
		"display_information": map[string]any{"name": "Slack MCP Local"},
		"oauth_config": map[string]any{
			"redirect_urls": []string{defaultRedirectURI},
			"pkce_enabled":  true,
			"scopes":        map[string]any{"user": scopes},
		},
		"settings": map[string]any{
			"org_deploy_enabled": false, "socket_mode_enabled": false, "token_rotation_enabled": true,
		},
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func customOAuthScopes(preset string) ([]string, error) {
	var tools []string
	switch preset {
	case "daily-power":
		tools = capability.DailyPowerLocalTools()
	case "legacy-full":
		tools = capability.LegacyFullLocalTools()
	default:
		return nil, fmt.Errorf("unknown preset %q (valid: daily-power, legacy-full)", preset)
	}
	scopes := capability.OAuthScopesForTools(tools)
	// Normal server startup warms user and conversation caches before serving
	// name-based tools, so these read scopes are runtime dependencies.
	scopes = append(scopes, "channels:read", "groups:read", "im:read", "mpim:read", "users:read")
	scopes = uniqueStrings(scopes)
	sort.Strings(scopes)
	return scopes, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
