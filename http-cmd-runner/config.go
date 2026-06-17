package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// utf8BOM is stripped from config files if present (e.g. files saved by some
// Windows editors), since the JSON parser rejects a leading BOM.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Config controls how the server binds, authenticates, and what it will run.
type Config struct {
	// Listen is the address to bind, e.g. "127.0.0.1:8080" or ":8080".
	Listen string `json:"listen"`

	// AllowArbitrary, when true, lets callers run any shell line via "sh -c".
	// When false (the default), only programs named in Allowlist may run, and
	// they are executed directly without a shell (no shell-injection surface).
	AllowArbitrary bool `json:"allow_arbitrary"`

	// Allowlist is the set of program names callers may invoke when
	// AllowArbitrary is false. Match is on the base command only; args are free.
	Allowlist []string `json:"allowlist"`

	// DefaultTimeoutSec applies when a request omits timeout_sec.
	DefaultTimeoutSec int `json:"default_timeout_sec"`

	// MaxTimeoutSec caps any caller-supplied timeout.
	MaxTimeoutSec int `json:"max_timeout_sec"`

	// WorkingDir is the directory commands run in. Defaults to the process cwd.
	WorkingDir string `json:"working_dir"`

	// MaxOutputBytes caps captured stdout/stderr to avoid unbounded memory use.
	MaxOutputBytes int `json:"max_output_bytes"`

	// TLSCertFile / TLSKeyFile enable HTTPS when both are set.
	TLSCertFile string `json:"tls_cert_file"`
	TLSKeyFile  string `json:"tls_key_file"`

	// LogFile, when set, appends an audit line per request. Empty = stdout.
	LogFile string `json:"log_file"`
}

func defaultConfig() Config {
	return Config{
		Listen:            "127.0.0.1:8080",
		AllowArbitrary:    true,
		Allowlist:         []string{},
		DefaultTimeoutSec: 30,
		MaxTimeoutSec:     300,
		MaxOutputBytes:    1 << 20, // 1 MiB
	}
}

// LoadConfig reads JSON config from path, layering it over the defaults, then
// applies environment overrides and validates the result.
func LoadConfig(path string) (Config, error) {
	cfg := defaultConfig()

	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %q: %w", path, err)
		}
		raw = bytes.TrimPrefix(raw, utf8BOM)
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %q: %w", path, err)
		}
	}

	if env := os.Getenv("CMDRUNNER_LISTEN"); env != "" {
		cfg.Listen = env
	}

	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if !c.AllowArbitrary && len(c.Allowlist) == 0 {
		return fmt.Errorf("allowlist is empty and allow_arbitrary is false: nothing could ever run")
	}
	if c.DefaultTimeoutSec <= 0 {
		return fmt.Errorf("default_timeout_sec must be > 0")
	}
	if c.MaxTimeoutSec < c.DefaultTimeoutSec {
		return fmt.Errorf("max_timeout_sec (%d) must be >= default_timeout_sec (%d)", c.MaxTimeoutSec, c.DefaultTimeoutSec)
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("tls_cert_file and tls_key_file must be set together")
	}
	return nil
}

// allows reports whether prog may be executed in allowlist mode.
func (c Config) allows(prog string) bool {
	for _, a := range c.Allowlist {
		if a == prog {
			return true
		}
	}
	return false
}
