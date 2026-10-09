// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package config

import (
	"os"
	"path/filepath"
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
