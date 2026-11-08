/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package appconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aliceHash is a bcrypt hash, as exporter-toolkit expects in basic_auth_users.
const aliceHash = "$2y$10$CWCEbs7mt8QFrToJR9OcG.tduEKpx9SROhWnaDHQgQX4Q6hqWNASa"

// writeWebConfig writes content as web.yml in dir and returns its path.
func writeWebConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "web.yml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// writeTestCertificate writes a self-signed certificate and key into dir.
func writeTestCertificate(t *testing.T, dir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tls.crt"), certPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tls.key"), keyPEM, 0o600))
}

func TestValidateProfilingWebConfig(t *testing.T) {
	tests := []struct {
		name      string
		content   string // empty means no web config file
		profiling bool
		wantField string // empty means no error
		wantText  string
	}{
		{name: "profiling off", content: "http_server_config:\n  http2: true\n"},
		{name: "no web config file", profiling: true},
		{name: "basic auth", content: "basic_auth_users:\n  alice: " + aliceHash + "\n", profiling: true},
		{name: "tls", content: "tls_server_config:\n  cert_file: tls.crt\n  key_file: tls.key\n", profiling: true},
		{
			name: "neither tls nor basic auth", content: "http_server_config:\n  http2: true\n", profiling: true,
			wantField: "EnablePprof", wantText: "turn on TLS (tls_server_config) or basic auth (basic_auth_users)",
		},
		{
			name: "empty users", content: "basic_auth_users: {}\n", profiling: true,
			wantField: "EnablePprof", wantText: "it turns on neither",
		},
		{
			name: "invalid file hides details", content: "basic_auth_users: [secret-marker\n", profiling: true,
			wantField: "WebConfigFile", wantText: "details suppressed to protect credentials",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestCertificate(t, dir)
			config := DefaultConfig()
			config.EnablePprof = test.profiling
			if test.content != "" {
				config.WebConfigFile = writeWebConfig(t, dir, test.content)
			}

			err := ValidateProfilingWebConfig(config)
			if test.wantField == "" {
				assert.NoError(t, err)
				return
			}

			setting := settingErrorOf(t, []error{err}, test.wantField)
			require.NotNil(t, setting, "error %v", err)
			assert.Contains(t, setting.Error(), test.wantText)
			assert.NotContains(t, setting.Error(), "secret-marker")
		})
	}
}

func TestValidateProfilingWebConfigNamesAMissingFile(t *testing.T) {
	config := DefaultConfig()
	config.EnablePprof = true
	config.WebConfigFile = filepath.Join(t.TempDir(), "missing.yml")

	err := ValidateProfilingWebConfig(config)
	setting := settingErrorOf(t, []error{err}, "WebConfigFile")
	require.NotNil(t, setting)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestValidateProfilingWebConfigNeedsFixHint(t *testing.T) {
	config := DefaultConfig()
	config.EnablePprof = true
	config.WebConfigFile = writeWebConfig(t, t.TempDir(), "http_server_config:\n  http2: true\n")

	var setting *SettingError
	require.ErrorAs(t, ValidateProfilingWebConfig(config), &setting)
	assert.NotEmpty(t, setting.Summary)
	assert.Contains(t, setting.Fix, "basic_auth_users")
}
