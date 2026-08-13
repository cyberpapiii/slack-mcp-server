package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/server"
	"github.com/korotovsky/slack-mcp-server/pkg/server/auth"
	"github.com/mattn/go-isatty"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var defaultSseHost = "127.0.0.1"
var defaultSsePort = 13080

func main() {
	var transport string
	var enabledToolsFlag string
	var toolPreset string
	var noCache bool
	flag.StringVar(&transport, "t", "stdio", "Transport type (stdio, sse or http)")
	flag.StringVar(&transport, "transport", "stdio", "Transport type (stdio, sse or http)")
	flag.StringVar(&enabledToolsFlag, "e", "", "Comma-separated list of enabled tools (overrides tool preset)")
	flag.StringVar(&enabledToolsFlag, "enabled-tools", "", "Comma-separated list of enabled tools (overrides tool preset)")
	flag.StringVar(&toolPreset, "tool-preset", "", "Tool preset: daily-power (default) or legacy-full")
	flag.BoolVar(&noCache, "no-cache", false, "Skip user/channel cache loading on startup for faster initialization. Lookups by #channel-name or @username will not work; use channel/user IDs instead.")
	flag.Parse()

	if enabledToolsFlag == "" {
		enabledToolsFlag = os.Getenv("SLACK_MCP_ENABLED_TOOLS")
	}
	if toolPreset == "" {
		toolPreset = os.Getenv("SLACK_MCP_TOOL_PRESET")
	}
	enabledTools, err := resolveEnabledTools(enabledToolsFlag, toolPreset)
	if err != nil {
		panic(err)
	}

	logger, err := newLogger(transport)
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	addMessageToolEnv := os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")
	err = validateToolConfig(addMessageToolEnv)
	if err != nil {
		logger.Fatal("error in SLACK_MCP_ADD_MESSAGE_TOOL",
			zap.String("context", "console"),
			zap.Error(err),
		)
	}
	reactionToolEnv := os.Getenv("SLACK_MCP_REACTION_TOOL")
	if err = validateToolConfig(reactionToolEnv); err != nil {
		logger.Fatal("error in SLACK_MCP_REACTION_TOOL",
			zap.String("context", "console"),
			zap.Error(err),
		)
	}

	err = server.ValidateEnabledTools(enabledTools)
	if err != nil {
		logger.Fatal("error in SLACK_MCP_ENABLED_TOOLS",
			zap.String("context", "console"),
			zap.Error(err),
		)
	}
	if err = applyResolvedToolPolicy(enabledTools); err != nil {
		logger.Fatal("failed to apply resolved tool policy", zap.Error(err))
	}

	p := provider.New(transport, logger)
	s := server.NewMCPServer(p, logger, enabledTools)
	logStartupAuthStatus(p, logger)

	if noCache {
		p.SkipCache()
		s.RegisterCacheDependentTools()
		logger.Info("Cache loading disabled via --no-cache flag",
			zap.String("context", "console"),
		)
	} else if isDemoCredentials() {
		// Register before Serve so early tools/list sees cache-dependent tools.
		p.SkipCache()
		s.RegisterCacheDependentTools()
		logger.Info("Demo credentials are set, skip cache warm-up",
			zap.String("context", "console"),
		)
	} else {
		startCacheWarmup(p, s, logger)
	}

	switch transport {
	case "stdio":
		if ready, _ := p.IsReady(); !ready {
			logger.Info("Slack MCP Server is still warming up caches, serving immediately",
				zap.String("context", "console"),
			)
		}
		if err := s.ServeStdio(); err != nil {
			logger.Fatal("Server error",
				zap.String("context", "console"),
				zap.Error(err),
			)
		}
	case "sse":
		if err := auth.RequireAPIKeyOrOptOut(logger); err != nil {
			logger.Fatal("Server error",
				zap.String("context", "console"),
				zap.Error(err),
			)
		}

		host, port := listenHostPort()
		addr := host + ":" + port
		sseServer := s.ServeSSE(addr)
		logger.Info(
			fmt.Sprintf("SSE server listening on %s:%s/sse", host, port),
			zap.String("context", "console"),
			zap.String("host", host),
			zap.String("port", port),
		)

		if ready, _ := p.IsReady(); !ready {
			logger.Info("Slack MCP Server is still warming up caches",
				zap.String("context", "console"),
			)
		}

		if err := sseServer.Start(addr); err != nil {
			logger.Fatal("Server error",
				zap.String("context", "console"),
				zap.Error(err),
			)
		}
	case "http":
		if err := auth.RequireAPIKeyOrOptOut(logger); err != nil {
			logger.Fatal("Server error",
				zap.String("context", "console"),
				zap.Error(err),
			)
		}

		host, port := listenHostPort()
		addr := host + ":" + port
		httpServer := s.ServeHTTP(addr)
		logger.Info(
			fmt.Sprintf("HTTP server listening on %s:%s", host, port),
			zap.String("context", "console"),
			zap.String("host", host),
			zap.String("port", port),
		)

		if ready, _ := p.IsReady(); !ready {
			logger.Info("Slack MCP Server is still warming up caches",
				zap.String("context", "console"),
			)
		}

		if err := httpServer.Start(addr); err != nil {
			logger.Fatal("Server error",
				zap.String("context", "console"),
				zap.Error(err),
			)
		}
	default:
		logger.Fatal("Invalid transport type",
			zap.String("context", "console"),
			zap.String("transport", transport),
			zap.String("allowed", "stdio, sse, http"),
		)
	}
}

func resolveEnabledTools(explicit, preset string) ([]string, error) {
	if strings.TrimSpace(explicit) != "" {
		var tools []string
		for _, tool := range strings.Split(explicit, ",") {
			if tool = strings.TrimSpace(tool); tool != "" {
				tools = append(tools, tool)
			}
		}
		if len(tools) == 0 {
			return nil, fmt.Errorf("SLACK_MCP_ENABLED_TOOLS / --enabled-tools is set but contains no tool names")
		}
		return tools, nil
	}
	if preset == "" {
		preset = "daily-power"
	}
	return server.ResolveToolPreset(preset)
}

// Handlers recheck mutation gates at call time. Keep that defensive check on
// the same resolved policy used for registration, including CLI presets and
// --enabled-tools overrides that did not originate in the environment.
func applyResolvedToolPolicy(enabledTools []string) error {
	return os.Setenv("SLACK_MCP_ENABLED_TOOLS", strings.Join(enabledTools, ","))
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

func validateToolConfig(config string) error {
	if config == "" || envutil.IsTruthy(config) {
		return nil
	}

	items := strings.Split(config, ",")
	hasNegated := false
	hasPositive := false
	validEntries := 0

	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || item == "!" {
			return fmt.Errorf("empty channel entry in allow/block list")
		}
		validEntries++
		if strings.HasPrefix(item, "!") {
			hasNegated = true
		} else {
			hasPositive = true
		}
	}

	if validEntries == 0 {
		return fmt.Errorf("channel allow/block list has no entries")
	}
	if hasNegated && hasPositive {
		return fmt.Errorf("cannot mix allowed and disallowed (! prefixed) channels")
	}

	return nil
}

func newLogger(transport string) (*zap.Logger, error) {
	atomicLevel := zap.NewAtomicLevelAt(zap.InfoLevel)
	if envLevel := os.Getenv("SLACK_MCP_LOG_LEVEL"); envLevel != "" {
		if err := atomicLevel.UnmarshalText([]byte(envLevel)); err != nil {
			fmt.Fprintf(os.Stderr, "Invalid log level '%s': %v, using 'info'\n", envLevel, err)
		}
	}

	useJSON := shouldUseJSONFormat()
	useColors := shouldUseColors() && !useJSON

	outputPath := "stdout"
	if transport == "stdio" {
		outputPath = "stderr"
	}

	var config zap.Config

	if useJSON {
		config = zap.Config{
			Level:            atomicLevel,
			Development:      false,
			Encoding:         "json",
			OutputPaths:      []string{outputPath},
			ErrorOutputPaths: []string{"stderr"},
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
			ErrorOutputPaths: []string{"stderr"},
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

	return logger, err
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
		return colorEnv == "true" || colorEnv == "1"
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
