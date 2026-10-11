// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package config loads girlpages settings from the system config file, the
// user's config file and GIRLPAGES_* environment variables, in that order.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// These are overridden at build time by the Makefile (-ldflags -X) so a
// /usr/local or packaged install can relocate them.
var (
	SysconfDir     = "/etc"
	SystemCacheDir = "/var/cache/girlpages"
)

// Backend names.
const (
	BackendAuto   = "auto"
	BackendOpenAI = "openai"
	BackendClaude = "claude"
)

// Duration is a time.Duration that reads from TOML strings like "10m".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	// Backend is "auto", "openai" or "claude".
	Backend string `toml:"backend"`
	// Jobs is how many LLM requests may run at once.
	Jobs int `toml:"jobs"`
	// ChunkSize is the largest piece of rendered man page text (in bytes)
	// sent to the model in one request. Long pages are split on section
	// boundaries.
	ChunkSize int `toml:"chunk_size"`
	// CacheDir overrides where generated pages are written.
	CacheDir string `toml:"cache_dir"`
	// ExtraInstructions is appended to the system prompt, for anyone who
	// wants to tune the vibe.
	ExtraInstructions string `toml:"extra_instructions"`
	// Pager overrides $GIRLPAGER/$PAGER.
	Pager string `toml:"pager"`
	// Batch picks the batch API bulk runs use: "openrouter", "anthropic"
	// or "none". Empty means openrouter if openai.batch_url is set, else
	// none.
	Batch string `toml:"batch"`

	OpenAI OpenAIConfig `toml:"openai"`
	Claude ClaudeConfig `toml:"claude"`
	// Anthropic is Anthropic's API, used for its Message Batches API.
	Anthropic AnthropicConfig `toml:"anthropic"`
	// Repo is a published girlpages repository to fetch pages from before
	// asking a model.
	Repo RepoConfig `toml:"repo"`

	// Files lists the config files that were actually read.
	Files []string `toml:"-"`
}

type OpenAIConfig struct {
	BaseURL     string            `toml:"base_url"`
	APIKey      string            `toml:"api_key"`
	APIKeyFile  string            `toml:"api_key_file"`
	Model       string            `toml:"model"`
	Temperature *float64          `toml:"temperature"`
	MaxTokens   int               `toml:"max_tokens"`
	Timeout     Duration          `toml:"timeout"`
	Headers     map[string]string `toml:"headers"`
	// BatchURL is an OpenRouter batch endpoint, such as
	// https://openrouter.ai/api/v1/batches. When set, bulk runs send their
	// pages as batches: slower, about half the price.
	BatchURL string `toml:"batch_url"`
	// BatchPoll is how often to check on a batch while waiting for it.
	BatchPoll Duration `toml:"batch_poll"`
}

type ClaudeConfig struct {
	Command string   `toml:"command"`
	Model   string   `toml:"model"`
	Args    []string `toml:"args"`
	Timeout Duration `toml:"timeout"`
}

type AnthropicConfig struct {
	APIKey     string `toml:"api_key"`
	APIKeyFile string `toml:"api_key_file"`
	Model      string `toml:"model"`
	// MaxTokens caps each reply, thinking included.
	MaxTokens   int               `toml:"max_tokens"`
	Temperature *float64          `toml:"temperature"`
	BatchURL    string            `toml:"batch_url"`
	Headers     map[string]string `toml:"headers"`
	Timeout     Duration          `toml:"timeout"`
}

// Batch APIs.
const (
	BatchNone       = "none"
	BatchOpenRouter = "openrouter"
	BatchAnthropic  = "anthropic"
)

const (
	DefaultOpenRouterBatchURL = "https://openrouter.ai/api/v1/batches"
	DefaultAnthropicBatchURL  = "https://api.anthropic.com/v1/messages/batches"
)

// ResolveBatch says which batch API bulk runs should use.
func (c *Config) ResolveBatch() (string, error) {
	switch strings.ToLower(strings.TrimSpace(c.Batch)) {
	case "", "auto":
		if strings.TrimSpace(c.OpenAI.BatchURL) != "" {
			return BatchOpenRouter, nil
		}
		return BatchNone, nil
	case BatchNone, "off", "no":
		return BatchNone, nil
	case BatchOpenRouter, "openai":
		return BatchOpenRouter, nil
	case BatchAnthropic, "claude":
		return BatchAnthropic, nil
	}
	return "", fmt.Errorf("batch = %q: want openrouter, anthropic or none", c.Batch)
}

// AnthropicKey returns the Anthropic API key from the config or the file
// it names.
func (c *Config) AnthropicKey() (string, error) {
	if c.Anthropic.APIKey != "" {
		return c.Anthropic.APIKey, nil
	}
	if c.Anthropic.APIKeyFile == "" {
		return "", errors.New("anthropic batches need an API key: set anthropic.api_key, anthropic.api_key_file or ANTHROPIC_API_KEY")
	}
	b, err := os.ReadFile(c.Anthropic.APIKeyFile)
	if err != nil {
		return "", fmt.Errorf("reading anthropic.api_key_file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

type RepoConfig struct {
	// URL is where the repository is: an http(s) URL, a file:// URL or a
	// directory. Empty means don't use one.
	URL     string   `toml:"url"`
	Timeout Duration `toml:"timeout"`
}

const DefaultOpenAIBaseURL = "https://api.openai.com/v1"

func Defaults() Config {
	return Config{
		Backend:   BackendAuto,
		Jobs:      4,
		ChunkSize: 12000,
		OpenAI: OpenAIConfig{
			BaseURL:   DefaultOpenAIBaseURL,
			Timeout:   Duration{10 * time.Minute},
			BatchPoll: Duration{time.Minute},
		},
		Claude: ClaudeConfig{
			Command: "claude",
			// Haiku is quick and cheap enough for thousands of pages and
			// keeps the details well; set model = "" for the CLI's default.
			Model:   "haiku",
			Timeout: Duration{15 * time.Minute},
		},
		Anthropic: AnthropicConfig{
			Model:     "claude-haiku-5-5",
			MaxTokens: 32000,
			BatchURL:  DefaultAnthropicBatchURL,
			Timeout:   Duration{10 * time.Minute},
		},
		Repo: RepoConfig{Timeout: Duration{20 * time.Second}},
	}
}

// SystemConfigPath is the system-wide config file.
func SystemConfigPath() string {
	return filepath.Join(SysconfDir, "girlpages", "config.toml")
}

// UserConfigPath is the per-user config file.
func UserConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "girlpages", "config.toml")
}

// UserCacheDir is the per-user cache directory.
func UserCacheDir() string {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "girlpages")
}

// Load reads the system config, then the user config (or explicit, if
// given, in its place), then the environment.
func Load(explicit string) (Config, error) {
	cfg := Defaults()
	if explicit == "" {
		explicit = os.Getenv("GIRLPAGES_CONFIG")
	}
	paths := []string{SystemConfigPath()}
	if explicit != "" {
		paths = append(paths, explicit)
	} else if p := UserConfigPath(); p != "" {
		paths = append(paths, p)
	}
	for _, p := range paths {
		if _, err := toml.DecodeFile(p, &cfg); err != nil {
			if errors.Is(err, os.ErrNotExist) && p != explicit {
				continue
			}
			if errors.Is(err, os.ErrPermission) && p != explicit {
				// A system config holding an API key may be root-only;
				// carry on without it.
				continue
			}
			return cfg, fmt.Errorf("reading %s: %w", p, err)
		}
		cfg.Files = append(cfg.Files, p)
	}
	if err := cfg.applyEnv(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() error {
	str := func(dst *string, names ...string) {
		for _, n := range names {
			if v, ok := os.LookupEnv(n); ok && v != "" {
				*dst = v
				return
			}
		}
	}
	str(&c.Backend, "GIRLPAGES_BACKEND")
	str(&c.CacheDir, "GIRLPAGES_CACHE_DIR")
	str(&c.OpenAI.BaseURL, "GIRLPAGES_OPENAI_BASE_URL", "OPENAI_BASE_URL")
	str(&c.OpenAI.APIKey, "GIRLPAGES_OPENAI_API_KEY", "OPENAI_API_KEY")
	str(&c.OpenAI.Model, "GIRLPAGES_OPENAI_MODEL")
	str(&c.OpenAI.BatchURL, "GIRLPAGES_OPENAI_BATCH_URL")
	str(&c.Batch, "GIRLPAGES_BATCH")
	str(&c.Anthropic.APIKey, "GIRLPAGES_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY")
	str(&c.Anthropic.Model, "GIRLPAGES_ANTHROPIC_MODEL")
	str(&c.Claude.Command, "GIRLPAGES_CLAUDE_COMMAND")
	str(&c.Claude.Model, "GIRLPAGES_CLAUDE_MODEL")
	str(&c.Repo.URL, "GIRLPAGES_REPO")
	if v := os.Getenv("GIRLPAGES_JOBS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("GIRLPAGES_JOBS: want a positive number, got %q", v)
		}
		c.Jobs = n
	}
	if v := os.Getenv("GIRLPAGES_MODEL"); v != "" {
		c.SetModel(v)
	}
	return nil
}

// SetModel sets the model for the selected backend (or both, when the
// backend is still "auto").
func (c *Config) SetModel(m string) {
	switch c.Backend {
	case BackendOpenAI:
		c.OpenAI.Model = m
	case BackendClaude:
		c.Claude.Model = m
	default:
		c.OpenAI.Model = m
		c.Claude.Model = m
	}
}

// ResolveBackend turns "auto" into a concrete backend: an explicitly
// configured OpenAI-compatible endpoint wins, then the claude CLI if it is
// on $PATH, then OpenAI proper if there is an API key.
func (c *Config) ResolveBackend() (string, error) {
	switch b := strings.ToLower(c.Backend); b {
	case BackendOpenAI, BackendClaude:
		return b, nil
	case "", BackendAuto:
	default:
		return "", fmt.Errorf("unknown backend %q (want auto, openai or claude)", c.Backend)
	}
	if c.OpenAI.BaseURL != "" && c.OpenAI.BaseURL != DefaultOpenAIBaseURL {
		return BackendOpenAI, nil
	}
	if _, err := exec.LookPath(c.Claude.Command); err == nil {
		return BackendClaude, nil
	}
	if c.OpenAI.APIKey != "" || c.OpenAI.APIKeyFile != "" {
		return BackendOpenAI, nil
	}
	return "", errors.New("no LLM backend configured: install the claude CLI, or set openai.base_url / OPENAI_API_KEY " +
		"in " + UserConfigPath() + " (see girl(1))")
}

// OpenAIKey returns the API key, reading api_key_file if needed.
func (c *Config) OpenAIKey() (string, error) {
	if c.OpenAI.APIKey != "" {
		return c.OpenAI.APIKey, nil
	}
	if c.OpenAI.APIKeyFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.OpenAI.APIKeyFile)
	if err != nil {
		return "", fmt.Errorf("reading openai.api_key_file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// WriteCacheDir is where new pages are stored: cache_dir if set, the system
// cache when running as root, otherwise the user's cache.
func (c *Config) WriteCacheDir() string {
	if c.CacheDir != "" {
		return c.CacheDir
	}
	if os.Geteuid() == 0 {
		return SystemCacheDir
	}
	if d := UserCacheDir(); d != "" {
		return d
	}
	return SystemCacheDir
}

// ReadCacheDirs lists every cache to look in, most preferred first.
func (c *Config) ReadCacheDirs() []string {
	dirs := []string{c.WriteCacheDir()}
	if dirs[0] != SystemCacheDir {
		dirs = append(dirs, SystemCacheDir)
	}
	return dirs
}
