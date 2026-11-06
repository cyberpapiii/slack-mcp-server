package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/server"
	"github.com/korotovsky/slack-mcp-server/pkg/server/auth"
	"github.com/mattn/go-isatty"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	transportList  = auth.TransportStdio + ", " + auth.TransportSSE + ", " + auth.TransportHTTP
	transportUsage = "Transport type (" + transportList + ")"
)

var defaultSseHost = "127.0.0.1"
var defaultSsePort = 13080

func main() {
	var transport string
	var enabledToolsFlag string
	var toolPreset string
	var noCache bool
	var checkKeychain bool
	flag.StringVar(&transport, "t", auth.TransportStdio, transportUsage)
	flag.StringVar(&transport, "transport", auth.TransportStdio, transportUsage)
	flag.StringVar(&enabledToolsFlag, "e", "", "Comma-separated list of enabled tools (overrides tool preset)")
	flag.StringVar(&enabledToolsFlag, "enabled-tools", "", "Comma-separated list of enabled tools (overrides tool preset)")
	flag.StringVar(&toolPreset, "tool-preset", "", "Tool preset: "+capability.PresetDailyPower+" (default) or "+capability.PresetLegacyFull)
	flag.BoolVar(&noCache, "no-cache", false, "Skip user/channel cache loading on startup for faster initialization. Lookups by #channel-name or @username will not work; use channel/user IDs instead.")
	flag.BoolVar(&checkKeychain, "check-keychain", false, "Read the Keychain items named by SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT and SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT, then exit. make deploy-local uses it to collect the macOS access approval before Plug starts the binary.")
	flag.Parse()

	if checkKeychain {
		os.Exit(runKeychainCheck())
	}

	if enabledToolsFlag == "" {
		enabledToolsFlag = os.Getenv("SLACK_MCP_ENABLED_TOOLS")
	}
	if toolPreset == "" {
		toolPreset = os.Getenv("SLACK_MCP_TOOL_PRESET")
	}
	logger, err := newLogger(transport)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	enabledTools, err := capability.Resolve(enabledToolsFlag, toolPreset)
	if err != nil {
		fatal(logger, "error in SLACK_MCP_ENABLED_TOOLS / SLACK_MCP_TOOL_PRESET", err)
	}

	settings, err := handler.SettingsFromEnv(os.Getenv)
	if err != nil {
		fatal(logger, "error in a channel allowlist", err)
	}
	for _, w := range settings.Warnings {
		logger.Warn(w, zap.String("context", "console"))
	}
	warnRemovedGateVars(logger)

	p, err := provider.New(logger)
	if err != nil {
		fatal(logger, "Failed to initialize Slack provider", err)
	}
	defer p.Close()
	s := server.NewMCPServer(p, transport, logger, enabledTools, settings)
	logStartupAuthStatus(p, logger)
	warnMissingOAuthScopes(p, enabledTools, logger)

	p.StartCaches(noCache, s.RegisterCacheDependentTools)

	if ready, _ := p.IsReady(); !ready {
		logger.Info("Slack MCP Server is still warming up caches, serving immediately",
			zap.String("context", "console"),
		)
	}

	switch transport {
	case auth.TransportStdio:
		if err := s.ServeStdio(); err != nil {
			fatal(logger, "Server error", err)
		}
	case auth.TransportSSE, auth.TransportHTTP:
		if err := auth.RequireAPIKeyOrOptOut(logger); err != nil {
			fatal(logger, "Server error", err)
		}
		host, port := listenHostPort()
		addr := host + ":" + port
		var srv interface{ Start(string) error }
		path := ""
		if transport == auth.TransportSSE {
			srv = s.ServeSSE(addr)
			path = "/sse"
		} else {
			srv = s.ServeHTTP(addr)
		}
		logger.Info(
			fmt.Sprintf("%s server listening on %s%s", strings.ToUpper(transport), addr, path),
			zap.String("context", "console"),
			zap.String("host", host),
			zap.String("port", port),
		)
		if err := srv.Start(addr); err != nil {
			fatal(logger, "Server error", err)
		}
	default:
		logger.Fatal("Invalid transport type",
			zap.String("context", "console"),
			zap.String("transport", transport),
			zap.String("allowed", transportList),
		)
	}
}

// logStartupAuthStatus warns when the browser session failed at startup. Only a
// session beside OAuth can start degraded, and its re-probe recovers it
// without a restart.
func logStartupAuthStatus(p *provider.ApiProvider, logger *zap.Logger) {
	if reason := p.BrowserDegradedReason(); reason != "" && !p.BrowserFeaturesAvailable() {
		logger.Warn("Browser session auth degraded: browser-session tools fail until the browser credential works again; the server retries every "+provider.BrowserReprobeEvery(),
			zap.String("context", "console"),
			zap.Strings("tools", capability.BrowserNames()),
			zap.String("reason", reason),
		)
	}
}

func fatal(logger *zap.Logger, msg string, err error) {
	logger.Fatal(msg, zap.String("context", "console"), zap.Error(err))
}

// warnMissingOAuthScopes warns when the managed OAuth login was granted fewer
// scopes than the served tools need: those tools fail with missing_scope
// until slack-mcp-auth logs in again for the same tool list.
func warnMissingOAuthScopes(p *provider.ApiProvider, enabled []string, logger *zap.Logger) {
	granted, ok := p.ManagedOAuthScopes()
	if !ok {
		return
	}
	if missing := missingOAuthScopes(granted, servedTools(p, enabled)); len(missing) > 0 {
		logger.Warn("The stored OAuth login lacks scopes enabled tools need; run slack-mcp-auth login --replace with the same SLACK_MCP_ENABLED_TOOLS, then restart",
			zap.String("context", "console"),
			zap.Strings("missing_scopes", missing),
		)
	}
}

// servedTools are the enabled tools the running credentials can serve.
func servedTools(creds capability.Credentials, enabled []string) []string {
	var served []string
	for _, name := range enabled {
		if tool, ok := capability.Lookup(name); ok && tool.Requires.Met(creds) {
			served = append(served, name)
		}
	}
	return served
}

func missingOAuthScopes(granted, tools []string) []string {
	var missing []string
	for _, scope := range capability.OAuthScopesForTools(tools) {
		if !slices.Contains(granted, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}

// warnRemovedGateVars flags SLACK_MCP_*_TOOL variables left over from the
// per-tool boolean gates, which are ignored now that the enabled-tools list is
// the single switch.
func warnRemovedGateVars(logger *zap.Logger) {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "SLACK_MCP_") || !strings.HasSuffix(name, "_TOOL") {
			continue
		}
		if slices.Contains(capability.ChannelAllowlists(), name) {
			continue
		}
		logger.Warn(name+" is ignored; tools are enabled only via SLACK_MCP_ENABLED_TOOLS or SLACK_MCP_TOOL_PRESET",
			zap.String("context", "console"),
		)
	}
}

func listenHostPort() (host, port string) {
	host = os.Getenv("SLACK_MCP_HOST")
	if host == "" {
		host = defaultSseHost
	}
	port = os.Getenv("SLACK_MCP_PORT")
	if port == "" {
		port = strconv.Itoa(defaultSsePort)
	}
	return host, port
}

func newLogger(transport string) (*zap.Logger, error) {
	atomicLevel := zap.NewAtomicLevelAt(zap.InfoLevel)
	envLevel := os.Getenv("SLACK_MCP_LOG_LEVEL")
	var levelErr error
	if envLevel != "" {
		levelErr = atomicLevel.UnmarshalText([]byte(envLevel))
	}

	useJSON := shouldUseJSONFormat()
	useColors := shouldUseColors() && !useJSON

	outputPath := "stdout"
	if transport == auth.TransportStdio {
		outputPath = "stderr"
	}
	errorPath := "stderr"
	logFile, err := logFilePath()
	if err != nil {
		return nil, err
	}
	if logFile != "" {
		outputPath, errorPath = logFile, logFile
	}

	var config zap.Config

	if useJSON {
		config = zap.Config{
			Level:            atomicLevel,
			Development:      false,
			Encoding:         "json",
			OutputPaths:      []string{outputPath},
			ErrorOutputPaths: []string{errorPath},
			EncoderConfig: zapcore.EncoderConfig{
				TimeKey:       "timestamp",
				LevelKey:      "level",
				NameKey:       "logger",
				MessageKey:    "message",
				StacktraceKey: "stacktrace",
				EncodeLevel:   zapcore.LowercaseLevelEncoder,
				EncodeTime:    zapcore.RFC3339TimeEncoder,
				EncodeCaller:  zapcore.ShortCallerEncoder,
			},
		}
	} else {
		config = zap.Config{
			Level:            atomicLevel,
			Development:      true,
			Encoding:         "console",
			OutputPaths:      []string{outputPath},
			ErrorOutputPaths: []string{errorPath},
			EncoderConfig: zapcore.EncoderConfig{
				TimeKey:          "timestamp",
				LevelKey:         "level",
				NameKey:          "logger",
				MessageKey:       "msg",
				StacktraceKey:    "stacktrace",
				EncodeLevel:      getConsoleLevelEncoder(useColors),
				EncodeTime:       zapcore.ISO8601TimeEncoder,
				EncodeCaller:     zapcore.ShortCallerEncoder,
				ConsoleSeparator: " | ",
			},
		}
	}

	logger, err := config.Build(zap.AddCaller())
	if err != nil {
		return nil, err
	}

	logger = logger.With(zap.String("app", "slack-mcp-server"))
	// Plug discards stderr, so the bad value is reported where the rest of
	// the log goes.
	if levelErr != nil {
		logger.Warn("SLACK_MCP_LOG_LEVEL is not a log level; using info", zap.String("value", envLevel), zap.Error(levelErr))
	}

	return logger, nil
}

func shouldUseJSONFormat() bool {
	if format := os.Getenv("SLACK_MCP_LOG_FORMAT"); format != "" {
		return strings.ToLower(format) == "json"
	}

	if env := os.Getenv("ENVIRONMENT"); env != "" {
		switch strings.ToLower(env) {
		case "production", "prod", "staging":
			return true
		case "development", "dev", "local":
			return false
		}
	}

	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" ||
		os.Getenv("DOCKER_CONTAINER") != "" ||
		os.Getenv("container") != "" {
		return true
	}

	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return true
	}

	return false
}

func shouldUseColors() bool {
	if colorEnv := os.Getenv("SLACK_MCP_LOG_COLOR"); colorEnv != "" {
		return envutil.IsTruthy(colorEnv)
	}

	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}

	return isatty.IsTerminal(os.Stdout.Fd())
}

func getConsoleLevelEncoder(useColors bool) zapcore.LevelEncoder {
	if useColors {
		return zapcore.CapitalColorLevelEncoder
	}
	return zapcore.CapitalLevelEncoder
}
