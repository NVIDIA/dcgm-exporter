/*
 * Unit tests for feature 001-multi-user-gpu-util YAML config loader.
 * Task T010: happy path / file missing / syntax / unknown-field / validation / defaults / static fallback.
 */

package appconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeTempYAML creates a temp config.yaml with the given content and returns its absolute path.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestLoadYAMLConfig_HappyPath(t *testing.T) {
	content := `
labels:
  static:
    - name: STUDIO
      value: "ai-lab"
  env:
    - name: PROJECT

server:
  port: ":9400"
  timeout: 10s
  read_timeout: 5s
  write_timeout: 10s
`
	cfg, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.NoError(t, err)
	require.Len(t, cfg.Labels.Static, 1)
	require.Equal(t, "STUDIO", cfg.Labels.Static[0].Name)
	require.Equal(t, "ai-lab", cfg.Labels.Static[0].ResolvedValue)
	require.Len(t, cfg.Labels.Env, 1)
	require.Equal(t, "PROJECT", cfg.Labels.Env[0].Name)
	require.Equal(t, "PROJECT", cfg.Labels.Env[0].EnvVar) // defaulted from Name
	require.Equal(t, ":9400", cfg.Server.Port)
}

func TestLoadYAMLConfig_FileMissing(t *testing.T) {
	_, err := LoadYAMLConfig("/tmp/does-not-exist-" + t.Name() + ".yaml")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrConfigNotFound), "expected ErrConfigNotFound, got %v", err)
}

func TestLoadYAMLConfig_MalformedYAML(t *testing.T) {
	_, err := LoadYAMLConfig(writeTempYAML(t, "labels: [: not valid :"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode")
}

func TestLoadYAMLConfig_TopLevelUnknownField(t *testing.T) {
	content := `
labels:
  static:
    - name: STUDIO
      value: ai-lab
kubernetes: true   # not allowed in this feature's config.yaml
`
	_, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.Error(t, err)
	require.Contains(t, err.Error(), "kubernetes")
}

func TestLoadYAMLConfig_NestedUnknownField(t *testing.T) {
	content := `
labels:
  static:
    - name: STUDIO
      value: ai-lab
  unknown_section:
    foo: bar
`
	_, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown_section")
}

func TestLoadYAMLConfig_ValidateBadLabelName(t *testing.T) {
	content := `
labels:
  static:
    - name: 9BAD
      value: x
`
	_, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.Error(t, err)
	require.Contains(t, err.Error(), "labels.static[0].name")
}

func TestLoadYAMLConfig_ValidateReservedName(t *testing.T) {
	content := `
labels:
  static:
    - name: USER
      value: alice
`
	_, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.Error(t, err)
	require.Contains(t, err.Error(), "system-reserved")
}

func TestLoadYAMLConfig_ValidateDuplicateNameAcrossSections(t *testing.T) {
	content := `
labels:
  static:
    - name: MYLBL
      value: x
  env:
    - name: MYLBL
`
	_, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate label name")
}

func TestLoadYAMLConfig_ValidateBadEnvVar(t *testing.T) {
	content := `
labels:
  env:
    - name: OK
      env_var: "123-bad"
`
	_, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.Error(t, err)
	require.Contains(t, err.Error(), "env_var")
}

func TestLoadYAMLConfig_ValidateBadPort(t *testing.T) {
	for _, bad := range []string{":0", ":99999", ":", "nosep", ":abc"} {
		t.Run(bad, func(t *testing.T) {
			content := "labels:\n  static:\n    - name: A\n      value: x\nserver:\n  port: \"" + bad + "\"\n"
			_, err := LoadYAMLConfig(writeTempYAML(t, content))
			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), "server.port")
		})
	}
}

func TestLoadYAMLConfig_ValidateNegativeTimeout(t *testing.T) {
	// Explicit negative durations are rejected. (Zero is indistinguishable from
	// "omitted" with time.Duration zero value, so 0s is treated as "use default".)
	content := `
labels:
  static:
    - name: A
      value: x
server:
  port: ":9400"
  timeout: -1s
`
	_, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.Error(t, err)
	require.Contains(t, err.Error(), "server.timeout")
}

func TestLoadYAMLConfig_DefaultsWhenLabelsEmpty(t *testing.T) {
	// FR-012: omit `labels` entirely -> synthesize [STUDIO, PROJECT].
	content := `
server:
  port: ":9400"
`
	cfg, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.NoError(t, err)
	require.Len(t, cfg.Labels.Static, 1)
	require.Equal(t, "STUDIO", cfg.Labels.Static[0].Name)
	require.Len(t, cfg.Labels.Env, 1)
	require.Equal(t, "PROJECT", cfg.Labels.Env[0].Name)
	require.Equal(t, "PROJECT", cfg.Labels.Env[0].EnvVar)
}

func TestLoadYAMLConfig_StaticValueFallbackToEnvVar(t *testing.T) {
	// FR-003: StaticLabel.Value empty -> read os.Getenv(name) -> else "unknown".
	t.Setenv("STUDIO", "dev-host")
	content := `
labels:
  static:
    - name: STUDIO
      value: ""
  env:
    - name: PROJECT
`
	cfg, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.NoError(t, err)
	require.Equal(t, "dev-host", cfg.Labels.Static[0].ResolvedValue)
}

func TestLoadYAMLConfig_StaticValueFallbackToUnknown(t *testing.T) {
	// Same as above but env var also unset -> final fallback "unknown".
	// Use a synthetic label name that's guaranteed not to exist in the environment.
	t.Setenv("LABEL_FOR_TEST_001_MUST_BE_UNSET", "") // ensure absent
	os.Unsetenv("LABEL_FOR_TEST_001_MUST_BE_UNSET")
	content := `
labels:
  static:
    - name: LABEL_FOR_TEST_001_MUST_BE_UNSET
      value: ""
  env:
    - name: PROJECT
`
	cfg, err := LoadYAMLConfig(writeTempYAML(t, content))
	require.NoError(t, err)
	require.Equal(t, FallbackUnknown, cfg.Labels.Static[0].ResolvedValue)
}

func TestLoadYAMLConfig_RepoRootExample(t *testing.T) {
	// Sanity: the canonical repo-root config.yaml must load+validate cleanly.
	// Path is relative to this test file: .../internal/pkg/appconfig -> ../../../../config.yaml
	cfg, err := LoadYAMLConfig("../../../config.yaml")
	require.NoError(t, err, "repo-root config.yaml must be a valid example")
	require.NotEmpty(t, cfg.Labels.Static)
	require.NotEmpty(t, cfg.Labels.Env)
	require.Equal(t, ":9400", cfg.Server.Port)
}
