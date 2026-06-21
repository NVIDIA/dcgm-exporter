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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	osmock "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/os"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	osinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/os"
)

// mockLocalHostname returns a hook that makes getLocalHostname resolve to name
// via os.Hostname() (with NODE_NAME unset).
func mockLocalHostname(t *testing.T, name string) func() func() {
	return func() func() {
		ctrl := gomock.NewController(t)
		m := osmock.NewMockOS(ctrl)
		m.EXPECT().Getenv(gomock.Eq("NODE_NAME"))
		m.EXPECT().Hostname().Return(name, nil).AnyTimes()
		os = m
		return func() {
			os = osinterface.RealOS{}
		}
	}
}

func TestGetHostname(t *testing.T) {
	tests := []struct {
		name    string
		config  *appconfig.Config
		hook    func() func()
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name:   "When os.Hostname() return hostname",
			config: &appconfig.Config{UseRemoteHE: false},
			hook: func() func() {
				ctrl := gomock.NewController(t)
				m := osmock.NewMockOS(ctrl)
				m.EXPECT().Getenv(gomock.Eq("NODE_NAME"))
				m.EXPECT().Hostname().Return("test-hostname", nil).AnyTimes()
				os = m
				return func() {
					os = osinterface.RealOS{}
				}
			},
			want: "test-hostname",
		},
		{
			name:   "When GetHostname uses the NODE_NAME env variable",
			config: &appconfig.Config{UseRemoteHE: false},
			hook: func() func() {
				ctrl := gomock.NewController(t)
				m := osmock.NewMockOS(ctrl)
				m.EXPECT().Getenv(gomock.Eq("NODE_NAME")).Return("test-hostname")
				os = m
				return func() {
					os = osinterface.RealOS{}
				}
			},
			want: "test-hostname",
		},
		{
			name:   "When os.Hostname() return error",
			config: &appconfig.Config{UseRemoteHE: false},
			hook: func() func() {
				ctrl := gomock.NewController(t)
				m := osmock.NewMockOS(ctrl)
				m.EXPECT().Getenv(gomock.Eq("NODE_NAME"))
				m.EXPECT().Hostname().Return("", errors.New("Boom!")).AnyTimes()
				os = m
				return func() {
					os = osinterface.RealOS{}
				}
			},
			want: "",
		},
		{
			name:   "When os.Hostname() return error",
			config: &appconfig.Config{UseRemoteHE: false},
			hook: func() func() {
				ctrl := gomock.NewController(t)
				m := osmock.NewMockOS(ctrl)
				m.EXPECT().Getenv(gomock.Eq("NODE_NAME"))
				m.EXPECT().Hostname().Return("", errors.New("Boom!")).AnyTimes()
				os = m
				return func() {
					os = osinterface.RealOS{}
				}
			},
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
			name: "When appconfig.UseRemoteHE is true and hostname is public IP address",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "192.168.1.1",
			},
			want: "192.168.1.1",
		},
		{
			name: "When appconfig.UseRemoteHE is true and hostname is loopback IP address",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "127.0.0.1",
			},
			hook: mockLocalHostname(t, "test-hostname"),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote is loopback IP with custom port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "127.0.0.1:5556",
			},
			hook: mockLocalHostname(t, "test-hostname"),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true, kubernetes is true, and hostname is localhost",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "localhost",
				Kubernetes:   true,
			},
			hook: func() func() {
				ctrl := gomock.NewController(t)
				m := osmock.NewMockOS(ctrl)
				m.EXPECT().Getenv(gomock.Eq("NODE_NAME"))
				m.EXPECT().Hostname().Return("test-hostname", nil).AnyTimes()
				os = m
				return func() {
					os = osinterface.RealOS{}
				}
			},
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote is localhost with custom port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "localhost:5555",
			},
			hook: mockLocalHostname(t, "test-hostname"),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote is localhost using NODE_NAME",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "localhost:5556",
			},
			hook: func() func() {
				ctrl := gomock.NewController(t)
				m := osmock.NewMockOS(ctrl)
				m.EXPECT().Getenv(gomock.Eq("NODE_NAME")).Return("node-from-env")
				os = m
				return func() {
					os = osinterface.RealOS{}
				}
			},
			want: "node-from-env",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is IPv6 loopback with port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "[::1]:5555",
			},
			hook: mockLocalHostname(t, "test-hostname"),
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
			name: "When appconfig.UseRemoteHE is true and remote address is IPv6 loopback without port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "[::1]",
			},
			hook: mockLocalHostname(t, "test-hostname"),
			want: "test-hostname",
		},
		{
			name: "When appconfig.UseRemoteHE is true and remote address is bare IPv6 loopback without brackets or port",
			config: &appconfig.Config{
				UseRemoteHE:  true,
				RemoteHEInfo: "::1",
			},
			hook: mockLocalHostname(t, "test-hostname"),
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
				cleanup := tt.hook()
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
