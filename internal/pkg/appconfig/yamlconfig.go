/*
 * Package appconfig: yamlconfig.go
 *
 * Implements feature-001 (multi-user GPU utilization) config.yaml loading,
 * defaulting, and validation. See spec 001-multi-user-gpu-util and
 * contracts/config.yaml.schema.md for the authoritative schema.
 *
 * Load sequence (called from pkg/cmd/app.go):
 *
 *    LoadYAMLConfig(path)
 *      |-- open & yaml.Decoder.KnownFields(true)
 *      |-- decode into *Config
 *      |-- ApplyDefaults()
 *      +-- Validate()  -> returns merged, validated *Config
 *
 * ErrConfigNotFound is returned when the YAML file does not exist, so the
 * caller can emit the exact log line mandated by FR-011.
 */

// Package appconfig holds the typed runtime configuration for dcgm-exporter,
// including the feature-001 (multi-user GPU utilization) `config.yaml`
// loader, validator, and defaulter.
package appconfig

import (
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

// ErrConfigNotFound is returned by LoadYAMLConfig when the config file cannot
// be stat'd. Callers should unwrap with errors.Is for the Q4 hard-fail path.
var ErrConfigNotFound = errors.New("config.yaml not found")

// labelNamePattern matches a valid Prometheus label name.
var labelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// shellEnvVarPattern matches a valid POSIX shell variable name.
var shellEnvVarPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// yamlDocument mirrors the only two top-level sections permitted in
// config.yaml for this feature. Decoding into this type (rather than directly
// into Config) lets us enable yaml.v3 KnownFields(true) strict mode without
// conflicting with the many fields on the upstream Config struct.
type yamlDocument struct {
	Labels LabelsConfig `yaml:"labels"`
	Server ServerConfig `yaml:"server"`
}

// LoadYAMLConfig reads `config.yaml` at path, strictly decodes it into a new
// *Config, applies built-in defaults, and validates the result.
//
// On success: caller receives a fully-populated *Config ready to merge with
// CLI overrides.
// Known failure modes:
//   - File missing / unstat'able  -> ErrConfigNotFound (wrapped with %q path)
//   - YAML syntax error           -> wrapped yaml.v3 error with line info
//   - Unknown top-level or nested field -> wrapped yaml.v3 error
//   - Validation rule failure     -> wrapped validation error (field path)
func LoadYAMLConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w at %s", ErrConfigNotFound, path)
		}
		return nil, fmt.Errorf("open config.yaml at %s: %w", path, err)
	}
	defer f.Close()

	var doc yamlDocument
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode config.yaml at %s: %w", path, err)
	}

	cfg := &Config{Labels: doc.Labels, Server: doc.Server}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config.yaml at %s: %w", path, err)
	}
	return cfg, nil
}

// ApplyDefaults fills the parts of Config that are derived rather than parsed:
//   - Labels: synthesize {STUDIO, PROJECT} when entirely absent (FR-012).
//   - Labels.Env[j].EnvVar: default to Name when empty.
//   - Labels.Static[i].ResolvedValue: resolve Value -> $Name -> "unknown".
//   - Server.*: apply DefaultServer* if the YAML omitted the field.
//
// ApplyDefaults must be called before Validate and is idempotent.
func (c *Config) ApplyDefaults() {
	// Labels defaults (FR-012).
	if len(c.Labels.Static) == 0 && len(c.Labels.Env) == 0 {
		c.Labels.Static = []StaticLabel{{Name: "STUDIO", Value: ""}}
		c.Labels.Env = []EnvLabel{{Name: "PROJECT", EnvVar: "PROJECT"}}
	}

	// StaticLabel startup fallback chain (FR-003).
	for i := range c.Labels.Static {
		resolved := c.Labels.Static[i].Value
		if resolved == "" {
			resolved = os.Getenv(c.Labels.Static[i].Name)
		}
		if resolved == "" {
			resolved = FallbackUnknown
		}
		c.Labels.Static[i].ResolvedValue = resolved
	}

	// EnvLabel: default EnvVar = Name.
	for i := range c.Labels.Env {
		if c.Labels.Env[i].EnvVar == "" {
			c.Labels.Env[i].EnvVar = c.Labels.Env[i].Name
		}
	}

	// Server defaults (apply only when zero-valued; preserve explicit negative
	// durations so Validate can reject them).
	if c.Server.Port == "" {
		c.Server.Port = DefaultServerPort
	}
	if c.Server.Timeout == 0 {
		c.Server.Timeout = DefaultServerTimeout
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = DefaultServerReadTimeout
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = DefaultServerWriteTimeout
	}
}

// Validate enforces every rule from contracts/config.yaml.schema.md that
// cannot be expressed by yaml.v3 schema alone. Returns a single error with a
// field-path-qualified message on the first violation; callers can print the
// message verbatim as FR-010 (c) mandates.
func (c *Config) Validate() error {
	reserved := SystemReservedLabelNames()
	seen := make(map[string]string, len(c.Labels.Static)+len(c.Labels.Env))

	for i, lbl := range c.Labels.Static {
		path := fmt.Sprintf("labels.static[%d]", i)
		if err := validateLabelName(path+".name", lbl.Name, reserved, seen); err != nil {
			return err
		}
	}
	for i, lbl := range c.Labels.Env {
		path := fmt.Sprintf("labels.env[%d]", i)
		if err := validateLabelName(path+".name", lbl.Name, reserved, seen); err != nil {
			return err
		}
		envVar := lbl.EnvVar
		if envVar == "" {
			envVar = lbl.Name
		}
		if !shellEnvVarPattern.MatchString(envVar) {
			return fmt.Errorf("%s.env_var: invalid shell variable name %q", path, envVar)
		}
	}

	if err := validatePort("server.port", c.Server.Port); err != nil {
		return err
	}
	// Negative durations are invalid; zero means "use default" and is handled
	// by ApplyDefaults before Validate sees the struct.
	if c.Server.Timeout < 0 {
		return fmt.Errorf("server.timeout: must be >= 0, got %s", c.Server.Timeout)
	}
	if c.Server.ReadTimeout < 0 {
		return fmt.Errorf("server.read_timeout: must be >= 0, got %s", c.Server.ReadTimeout)
	}
	if c.Server.WriteTimeout < 0 {
		return fmt.Errorf("server.write_timeout: must be >= 0, got %s", c.Server.WriteTimeout)
	}
	return nil
}

// validateLabelName checks Prometheus naming, reserved-word conflict, and
// cross-section uniqueness. `seen` is updated on success.
func validateLabelName(path, name string, reserved map[string]struct{}, seen map[string]string) error {
	if !labelNamePattern.MatchString(name) {
		return fmt.Errorf("%s: invalid label name %q (must match [a-zA-Z_][a-zA-Z0-9_]*)", path, name)
	}
	if _, isReserved := reserved[name]; isReserved {
		return fmt.Errorf("%s: %q is a system-reserved label name", path, name)
	}
	if prev, dup := seen[name]; dup {
		return fmt.Errorf("%s: duplicate label name %q (already declared at %s)", path, name, prev)
	}
	seen[name] = path
	return nil
}

// validatePort accepts "[host]:port" form. Empty host is fine (":9400").
func validatePort(path, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s: empty port", path)
	}
	_, portStr, err := net.SplitHostPort(raw)
	if err != nil {
		return fmt.Errorf("%s: cannot parse %q as host:port: %w", path, raw, err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("%s: non-numeric port %q", path, portStr)
	}
	if p < 1 || p > 65535 {
		return fmt.Errorf("%s: port %d out of range [1,65535]", path, p)
	}
	return nil
}
