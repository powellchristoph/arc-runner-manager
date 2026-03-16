package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// Config holds all runtime configuration for the API server.
type Config struct {
	// Server
	ListenAddr string

	// APITokens is the set of valid tokens keyed by name.
	// Loaded from API_TOKENS (JSON) with API_KEY as a single-token fallback.
	// Example API_TOKENS value:
	//   {"frontend":"tok-abc123","ci-system":"tok-def456","admin":"tok-xyz789"}
	APITokens map[string]string

	// Helm / ARC defaults
	ARCChartRepo    string
	ARCChartName    string
	ARCChartVersion string
	ControllerNS    string

	// Runner defaults applied when team omits a field
	DefaultMinRunners    int
	DefaultMaxRunners    int
	DefaultRunnerImage   string
	DefaultStorageClass  string
	DefaultStorageSize   string
	DefaultCPURequest    string
	DefaultCPULimit      string
	DefaultMemoryRequest string
	DefaultMemoryLimit   string
}

func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:           getEnv("LISTEN_ADDR", ":8080"),
		ARCChartRepo:         getEnv("ARC_CHART_REPO", "oci://ghcr.io/actions/actions-runner-controller-charts"),
		ARCChartName:         getEnv("ARC_CHART_NAME", "gha-runner-scale-set"),
		ARCChartVersion:      getEnv("ARC_CHART_VERSION", "0.9.3"),
		ControllerNS:         getEnv("ARC_CONTROLLER_NS", "arc-system"),
		DefaultMinRunners:    getEnvInt("DEFAULT_MIN_RUNNERS", 0),
		DefaultMaxRunners:    getEnvInt("DEFAULT_MAX_RUNNERS", 10),
		DefaultRunnerImage:   getEnv("DEFAULT_RUNNER_IMAGE", "ghcr.io/actions/actions-runner:2.317.0"),
		DefaultStorageClass:  getEnv("DEFAULT_STORAGE_CLASS", "standard"),
		DefaultStorageSize:   getEnv("DEFAULT_STORAGE_SIZE", "1Gi"),
		DefaultCPURequest:    getEnv("DEFAULT_CPU_REQUEST", "500m"),
		DefaultCPULimit:      getEnv("DEFAULT_CPU_LIMIT", "2"),
		DefaultMemoryRequest: getEnv("DEFAULT_MEMORY_REQUEST", "1Gi"),
		DefaultMemoryLimit:   getEnv("DEFAULT_MEMORY_LIMIT", "4Gi"),
	}

	tokens, err := loadTokens()
	if err != nil {
		return nil, err
	}
	cfg.APITokens = tokens
	return cfg, nil
}

// loadTokens builds the token map from environment variables.
//
// Priority:
//  1. API_TOKENS — JSON object mapping name → token value.
//     Example: {"frontend":"tok-abc","ci":"tok-def","admin":"tok-xyz"}
//  2. API_KEY    — legacy single-token fallback, treated as name "default".
//
// At least one token must be configured or the server refuses to start.
func loadTokens() (map[string]string, error) {
	if raw := os.Getenv("API_TOKENS"); raw != "" {
		var tokens map[string]string
		if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
			return nil, fmt.Errorf("API_TOKENS: invalid JSON: %w", err)
		}
		if len(tokens) == 0 {
			return nil, fmt.Errorf("API_TOKENS: must contain at least one token")
		}
		for name, tok := range tokens {
			if tok == "" {
				return nil, fmt.Errorf("API_TOKENS: token %q has empty value", name)
			}
		}
		return tokens, nil
	}

	// Legacy fallback: single API_KEY becomes {"default": "<key>"}
	if key := os.Getenv("API_KEY"); key != "" {
		return map[string]string{"default": key}, nil
	}

	fmt.Fprintln(os.Stderr, "FATAL: set API_TOKENS (JSON map) or API_KEY")
	os.Exit(1)
	return nil, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
