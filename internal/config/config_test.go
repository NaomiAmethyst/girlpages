// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadLayers(t *testing.T) {
	tmp := t.TempDir()
	SysconfDir = filepath.Join(tmp, "etc")
	t.Cleanup(func() { SysconfDir = "/etc" })
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "home"))
	for _, k := range []string{"GIRLPAGES_CONFIG", "GIRLPAGES_BACKEND", "GIRLPAGES_MODEL", "OPENAI_API_KEY",
		"OPENAI_BASE_URL", "GIRLPAGES_OPENAI_API_KEY", "GIRLPAGES_OPENAI_BASE_URL", "GIRLPAGES_JOBS"} {
		t.Setenv(k, "")
	}

	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(SystemConfigPath(), "backend = \"openai\"\njobs = 2\n[openai]\nbase_url = \"http://sys/v1\"\nmodel = \"sys-model\"\ntimeout = \"30s\"\n")
	write(UserConfigPath(), "jobs = 6\n[openai]\nmodel = \"user-model\"\n")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend != "openai" || cfg.Jobs != 6 || cfg.OpenAI.BaseURL != "http://sys/v1" ||
		cfg.OpenAI.Model != "user-model" || cfg.OpenAI.Timeout.Duration != 30*time.Second || len(cfg.Files) != 2 {
		t.Fatalf("cfg = %+v", cfg)
	}

	t.Setenv("GIRLPAGES_MODEL", "env-model")
	t.Setenv("GIRLPAGES_JOBS", "9")
	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenAI.Model != "env-model" || cfg.Jobs != 9 {
		t.Fatalf("env not applied: %+v", cfg)
	}

	if _, err := Load(filepath.Join(tmp, "missing.toml")); err == nil {
		t.Error("explicit missing config should be an error")
	}
}

func TestResolveBackend(t *testing.T) {
	c := Defaults()
	c.Claude.Command = "definitely-not-a-real-command-girlpages"
	if _, err := c.ResolveBackend(); err == nil {
		t.Error("expected an error with nothing configured")
	}
	c.OpenAI.APIKey = "sk"
	if b, _ := c.ResolveBackend(); b != BackendOpenAI {
		t.Errorf("got %q", b)
	}
	c.OpenAI.APIKey = ""
	c.OpenAI.BaseURL = "http://localhost:11434/v1"
	if b, _ := c.ResolveBackend(); b != BackendOpenAI {
		t.Errorf("got %q", b)
	}
	c.Backend = "nope"
	if _, err := c.ResolveBackend(); err == nil {
		t.Error("unknown backend accepted")
	}
}

func TestDefaultClaudeModel(t *testing.T) {
	if m := Defaults().Claude.Model; m != "haiku" {
		t.Errorf("default claude model = %q", m)
	}
}

func TestResolveAPI(t *testing.T) {
	for _, c := range []struct{ api, window, want string }{
		{"", "", APIChat},
		{"chat", "balanced", APIChat},
		{"responses", "flex", APIResponses},
		{"Responses", "", APIResponses},
		{"chat", "flex", ""},
		{"responses", "eventually", ""},
		{"soap", "", ""},
	} {
		cfg := Defaults()
		cfg.OpenAI.API, cfg.OpenAI.CompletionWindow = c.api, c.window
		got, err := cfg.ResolveAPI()
		if got != c.want || (err != nil) != (c.want == "") {
			t.Errorf("api %q, window %q: %q, %v", c.api, c.window, got, err)
		}
	}
}

func TestResolveBatch(t *testing.T) {
	cases := []struct {
		batch, url, want string
	}{
		{"", "", BatchNone},
		{"", "https://openrouter.ai/api/v1/batches", BatchOpenRouter},
		{"anthropic", "https://openrouter.ai/api/v1/batches", BatchAnthropic},
		{"none", "https://openrouter.ai/api/v1/batches", BatchNone},
		{"OpenRouter", "", BatchOpenRouter},
		{"sail", "", BatchSail},
		{"bogus", "", ""},
	}
	for _, c := range cases {
		cfg := Defaults()
		cfg.Batch, cfg.OpenAI.BatchURL = c.batch, c.url
		got, err := cfg.ResolveBatch()
		if got != c.want || (err != nil) != (c.want == "") {
			t.Errorf("batch %q, url %q: %q, %v", c.batch, c.url, got, err)
		}
	}
	cfg := Defaults()
	if _, err := cfg.AnthropicKey(); err == nil {
		t.Error("no anthropic key should be an error")
	}
	if cfg.Anthropic.Model != "claude-haiku-5-5" || cfg.Anthropic.BatchURL != DefaultAnthropicBatchURL {
		t.Errorf("anthropic defaults: %+v", cfg.Anthropic)
	}
}

func TestProviders(t *testing.T) {
	tmp := t.TempDir()
	SysconfDir = filepath.Join(tmp, "etc")
	t.Cleanup(func() { SysconfDir = "/etc" })
	for _, k := range []string{"GIRLPAGES_CONFIG", "GIRLPAGES_BACKEND", "GIRLPAGES_MODEL", "GIRLPAGES_PROVIDER",
		"OPENAI_BASE_URL", "GIRLPAGES_OPENAI_API_KEY", "GIRLPAGES_OPENAI_BASE_URL", "GIRLPAGES_BATCH"} {
		t.Setenv(k, "")
	}
	// An OpenAI key in the environment is for [openai], never a provider.
	t.Setenv("OPENAI_API_KEY", "sk-from-env")
	p := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(p, []byte(`backend = "openai"
batch = "openrouter"
[openai]
base_url = "https://openrouter.ai/api/v1"
model = "plain"
headers = {"X-Plain" = "1"}
[providers.sail]
base_url = "https://api.sailresearch.com/v1"
model = "deepseek-ai/DeepSeek-V4.1-Flash"
api_key_file = "/run/sail-key"
api = "responses"
completion_window = "flex"
max_tokens = 32000
batch = "sail"
[providers.haiku]
backend = "claude"
model = "claude-haiku-5-5"
args = ["--effort", "low"]
[providers.local]
base_url = "http://localhost:11434/v1"
[providers.claudebatch]
backend = "claude"
batch = "anthropic"
batch_max_requests = 500
api_key_file = "/run/anthropic-key"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "" || cfg.OpenAI.APIKey != "sk-from-env" || cfg.OpenAI.Model != "plain" {
		t.Fatalf("without a provider: %+v", cfg.OpenAI)
	}
	if fmt.Sprint(cfg.ProviderNames()) != "[claudebatch haiku local sail]" {
		t.Errorf("names: %v", cfg.ProviderNames())
	}

	sail := cfg
	if err := sail.UseProvider("sail"); err != nil {
		t.Fatal(err)
	}
	o := sail.OpenAI
	if sail.Backend != BackendOpenAI || sail.Provider != "sail" || o.BaseURL != "https://api.sailresearch.com/v1" ||
		o.Model != "deepseek-ai/DeepSeek-V4.1-Flash" || o.APIKey != "" || o.APIKeyFile != "/run/sail-key" ||
		o.API != "responses" || o.CompletionWindow != "flex" || o.MaxTokens != 32000 || len(o.Headers) != 0 ||
		o.Timeout.Duration != 10*time.Minute || sail.Batch != BatchSail {
		t.Fatalf("sail: backend %q batch %q %+v", sail.Backend, sail.Batch, o)
	}

	haiku := cfg
	if err := haiku.UseProvider("haiku"); err != nil {
		t.Fatal(err)
	}
	if haiku.Backend != BackendClaude || haiku.Claude.Model != "claude-haiku-5-5" || haiku.Claude.Command != "claude" ||
		fmt.Sprint(haiku.Claude.Args) != "[--effort low]" || haiku.Batch != BatchNone {
		t.Fatalf("haiku: %+v, batch %q", haiku.Claude, haiku.Batch)
	}
	haiku.SetModel("claude-opus-5-5") // --model after --provider
	if haiku.Claude.Model != "claude-opus-5-5" {
		t.Errorf("--model: %q", haiku.Claude.Model)
	}

	local := cfg
	if err := local.UseProvider("local"); err != nil {
		t.Fatal(err)
	}
	if local.OpenAI.BaseURL != "http://localhost:11434/v1" || local.OpenAI.Model != "" || local.OpenAI.APIKey != "" {
		t.Fatalf("local: %+v", local.OpenAI)
	}

	// Batches belong to the provider: the top-level batch (openrouter,
	// here) never applies to one.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-env")
	cb := cfg
	cb.BatchMaxRequests = 3000
	if err := cb.UseProvider("claudebatch"); err != nil {
		t.Fatal(err)
	}
	if cb.Batch != BatchAnthropic || cb.BatchMaxRequests != 500 || cb.Anthropic.APIKeyFile != "/run/anthropic-key" || cb.Anthropic.APIKey != "" ||
		cb.Anthropic.Model != "claude-haiku-5-5" {
		t.Fatalf("claudebatch: batch %q, max %d, %+v", cb.Batch, cb.BatchMaxRequests, cb.Anthropic)
	}
	if local.Batch != BatchNone || local.BatchMaxRequests != 0 {
		t.Errorf("local batches: %q, %d", local.Batch, local.BatchMaxRequests)
	}

	if err := cfg.UseProvider("nope"); err == nil || !strings.Contains(err.Error(), "claudebatch, haiku, local, sail") {
		t.Errorf("nope: %v", err)
	}

	// provider = "…" in the file, or GIRLPAGES_PROVIDER, picks one at load.
	t.Setenv("GIRLPAGES_PROVIDER", "sail")
	if cfg, err := Load(p); err != nil || cfg.Provider != "sail" || cfg.OpenAI.APIKey != "" {
		t.Fatalf("GIRLPAGES_PROVIDER: %+v, %v", cfg.OpenAI, err)
	}
	t.Setenv("GIRLPAGES_PROVIDER", "nope")
	if _, err := Load(p); err == nil {
		t.Error("GIRLPAGES_PROVIDER=nope loaded")
	}
}
