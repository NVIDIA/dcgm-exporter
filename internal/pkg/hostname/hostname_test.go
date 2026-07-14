/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package hostname

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	osmock "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/os"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	osinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/os"
)

func TestGetHostname(t *testing.T) {
	mockLocalHostname := func(hostname string, err error) func(*testing.T) func() {
		return func(t *testing.T) func() {
			t.Helper()
			ctrl := gomock.NewController(t)
			m := osmock.NewMockOS(ctrl)
			m.EXPECT().Getenv(gomock.Eq("NODE_NAME"))
			m.EXPECT().Hostname().Return(hostname, err).AnyTimes()
			os = m
			return func() {
				os = osinterface.RealOS{}
			}
		}
	}

	mockNodeName := func(nodeName string) func(*testing.T) func() {
		return func(t *testing.T) func() {
			t.Helper()
			ctrl := gomock.NewController(t)
			m := osmock.NewMockOS(ctrl)
			m.EXPECT().Getenv(gomock.Eq("NODE_NAME")).Return(nodeName)
			os = m
			return func() {
				os = osinterface.RealOS{}
			}
		}
	}

	tests := []struct {
		name    string
		config  *appconfig.Config
		hook    func(*testing.T) func()
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name:   "When os.Hostname() return hostname",
			config: &appconfig.Config{UseRemoteHE: false},
			hook:   mockLocalHostname("test-hostname", nil),
			want:   "test-hostname",
		},
		{
			name:   "When GetHostname uses the NODE_NAME env variable",
			config: &appconfig.Config{UseRemoteHE: false},
			hook:   mockNodeName("test-hostname"),
			want:   "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote loopback hostname local lookup returns error",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "localhost:5555",
			},
			hook:    mockLocalHostname("", errors.New("Boom!")),
			want:    "",
			wantErr: assert.Error,
		},
		{
			name:    "When os.Hostname() return error",
			config:  &appconfig.Config{UseRemoteHE: false},
			hook:    mockLocalHostname("", errors.New("Boom!")),
			want:    "",
			wantErr: assert.Error,
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote hostname is name",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "example.com:5555",
			},
			want: "example.com",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote hostname is loopback IP address",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "127.0.0.1",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote hostname is non-loopback IP address",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "192.168.1.1",
			},
			want: "192.168.1.1",
		},
		{
			name: "When appconfig.UseRemoteHE is true, kubernetes is true, and hostname is localhost",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "localhost",
				Kubernetes:   true,
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote hostname is loopback name with port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "localhost:5555",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote hostname is uppercase loopback name with port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "LOCALHOST:5555",
			},
			hook: mockNodeName("test-node"),
			want: "test-node",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote hostname is loopback IP address with port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "127.0.0.1:5555",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is IPv6 loopback with port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "[::1]:5555",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is full IPv6 with port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "[2001:db8::1]:5555",
			},
			want: "2001:db8::1",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is IPv6 wildcard with port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "[::]:5555",
			},
			want: "::",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is IPv6 without port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "[::1]",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is bare IPv6 without brackets or port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "::1",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is TCP URI",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "tcp://example.com:5555",
			},
			want: "example.com",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is uppercase TCP URI",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "TCP://example.com:5555",
			},
			want: "example.com",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is TCP loopback URI",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "tcp://127.0.0.1:5555",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is TCP IPv6 loopback URI",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "tcp://[::1]:5555",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is UNIX URI",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "unix:///tmp/dcgm.sock",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is uppercase UNIX URI",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "UNIX:///tmp/dcgm.sock",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is VSOCK URI",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "vsock://3:5555",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is uppercase VSOCK URI",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "VSOCK://3:5555",
			},
			hook: mockLocalHostname("test-hostname", nil),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is empty",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "",
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.hook != nil {
				cleanup := tt.hook(t)
				defer cleanup()
			}
			got, err := GetHostname(tt.config)
			if tt.wantErr != nil && !tt.wantErr(t, err, fmt.Sprintf("GetHostname(%v)", tt.config)) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetHostname(%v)", tt.config)
		})
	}
}

func FuzzParseRemoteHostname(f *testing.F) {
	for _, seed := range []string{
		"example.com:5555",
		"localhost:5555",
		"tcp://[2001:db8::1]:5555",
		"tcp://127.0.0.1:5555",
		"unix:///tmp/dcgm.sock",
		"vsock://3:5555",
		"::1",
		"",
	} {
		f.Add(seed)
	}

	f.Setenv("NODE_NAME", "fuzz-local-node")

	f.Fuzz(func(t *testing.T, remoteHEInfo string) {
		config := &appconfig.Config{UseRemoteHE: true, RemoteHEInfo: remoteHEInfo}
		first, err := parseRemoteHostname(config)
		if err != nil {
			t.Fatalf("hostname parsing failed with deterministic local hostname: %v", err)
		}
		second, err := parseRemoteHostname(config)
		if err != nil || first != second {
			t.Fatalf("hostname parsing is not deterministic: first=%q second=%q err=%v", first, second, err)
		}

		u, parseErr := url.Parse(remoteHEInfo)
		if parseErr != nil {
			return
		}
		switch {
		case strings.EqualFold(u.Scheme, "unix"), strings.EqualFold(u.Scheme, "vsock"):
			if first != "fuzz-local-node" {
				t.Fatalf("local transport returned hostname %q", first)
			}
		case strings.EqualFold(u.Scheme, "tcp") && u.Hostname() != "":
			expected := u.Hostname()
			if isLoopbackHost(expected) {
				expected = "fuzz-local-node"
			}
			if first != expected {
				t.Fatalf("TCP hostname=%q, expected %q", first, expected)
			}
		}
	})
}
