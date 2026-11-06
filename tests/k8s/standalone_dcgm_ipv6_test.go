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

package k8s

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

// validateRemoteDcgmIPv6Service requires a single-stack IPv6 Service with one IPv6 cluster IP.
func validateRemoteDcgmIPv6Service(output []byte) error {
	var service corev1.Service
	if err := json.Unmarshal(output, &service); err != nil {
		return fmt.Errorf("parse standalone DCGM Service: %w", err)
	}
	policy := ""
	if service.Spec.IPFamilyPolicy != nil {
		policy = string(*service.Spec.IPFamilyPolicy)
	}
	if policy != string(corev1.IPFamilyPolicySingleStack) {
		return fmt.Errorf("standalone DCGM Service IP family policy = %q, want SingleStack", policy)
	}
	if len(service.Spec.IPFamilies) != 1 || service.Spec.IPFamilies[0] != corev1.IPv6Protocol {
		return fmt.Errorf("standalone DCGM Service IP families = %v, want [IPv6]", service.Spec.IPFamilies)
	}
	if len(service.Spec.ClusterIPs) != 1 || !isIPv6(service.Spec.ClusterIPs[0]) {
		return fmt.Errorf("standalone DCGM Service cluster IPs = %v, want one IPv6 address", service.Spec.ClusterIPs)
	}
	return nil
}

// validateRemoteDcgmIPv6Endpoints requires IPv6 EndpointSlices with at least one ready IPv6 address.
func validateRemoteDcgmIPv6Endpoints(output []byte) error {
	var slices discoveryv1.EndpointSliceList
	if err := json.Unmarshal(output, &slices); err != nil {
		return fmt.Errorf("parse standalone DCGM EndpointSlices: %w", err)
	}
	foundReadyIPv6 := false
	for _, slice := range slices.Items {
		if slice.AddressType != discoveryv1.AddressTypeIPv6 {
			return fmt.Errorf("standalone DCGM EndpointSlice address type = %q, want IPv6", slice.AddressType)
		}
		for _, endpoint := range slice.Endpoints {
			if readyIPv6Endpoint(endpoint) {
				foundReadyIPv6 = true
			}
		}
	}
	if !foundReadyIPv6 {
		return fmt.Errorf("standalone DCGM has no ready IPv6 EndpointSlice address")
	}
	return nil
}

// readyIPv6Endpoint reports whether an endpoint that is not marked unready has an
// IPv6 address. Kubernetes treats a missing ready condition as ready.
func readyIPv6Endpoint(endpoint discoveryv1.Endpoint) bool {
	if ready := endpoint.Conditions.Ready; ready != nil && !*ready {
		return false
	}
	for _, address := range endpoint.Addresses {
		if isIPv6(address) {
			return true
		}
	}
	return false
}

// isIPv6 reports whether an address parses as IPv6 rather than IPv4.
func isIPv6(address string) bool {
	parsed := net.ParseIP(address)
	return parsed != nil && parsed.To4() == nil
}

// TestValidateRemoteDcgmIPv6Service checks accepted IPv6 and rejected dual-stack or IPv4 Services.
func TestValidateRemoteDcgmIPv6Service(t *testing.T) {
	policy := corev1.IPFamilyPolicySingleStack
	tests := []struct {
		name    string
		service corev1.Service
		wantErr bool
	}{
		{name: "IPv6 single stack", service: corev1.Service{Spec: corev1.ServiceSpec{IPFamilyPolicy: &policy, IPFamilies: []corev1.IPFamily{corev1.IPv6Protocol}, ClusterIPs: []string{"fd00::1"}}}},
		{name: "dual stack", service: corev1.Service{Spec: corev1.ServiceSpec{IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}}}, wantErr: true},
		{name: "IPv4 cluster IP", service: corev1.Service{Spec: corev1.ServiceSpec{IPFamilyPolicy: &policy, IPFamilies: []corev1.IPFamily{corev1.IPv6Protocol}, ClusterIPs: []string{"192.0.2.1"}}}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := json.Marshal(test.service)
			if err != nil {
				t.Fatal(err)
			}
			err = validateRemoteDcgmIPv6Service(output)
			if test.wantErr && err == nil {
				t.Fatal("validateRemoteDcgmIPv6Service() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateRemoteDcgmIPv6Service() error = %v", err)
			}
		})
	}
}

// TestValidateRemoteDcgmIPv6Endpoints checks ready, omitted-readiness, unready, and IPv4 endpoints.
func TestValidateRemoteDcgmIPv6Endpoints(t *testing.T) {
	ready := true
	unready := false
	tests := []struct {
		name    string
		slices  discoveryv1.EndpointSliceList
		wantErr bool
	}{
		{
			name: "ready IPv6 endpoint",
			slices: discoveryv1.EndpointSliceList{Items: []discoveryv1.EndpointSlice{{
				AddressType: discoveryv1.AddressTypeIPv6,
				Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"2001:db8::1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
			}}},
		},
		{
			name: "IPv4 endpoint",
			slices: discoveryv1.EndpointSliceList{Items: []discoveryv1.EndpointSlice{{
				AddressType: discoveryv1.AddressTypeIPv4,
				Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"192.0.2.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
			}}},
			wantErr: true,
		},
		{
			name: "IPv6 endpoint with omitted ready condition",
			slices: discoveryv1.EndpointSliceList{Items: []discoveryv1.EndpointSlice{{
				AddressType: discoveryv1.AddressTypeIPv6,
				Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"2001:db8::1"}}},
			}}},
		},
		{
			name: "unready IPv6 endpoint",
			slices: discoveryv1.EndpointSliceList{Items: []discoveryv1.EndpointSlice{{
				AddressType: discoveryv1.AddressTypeIPv6,
				Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"2001:db8::1"}, Conditions: discoveryv1.EndpointConditions{Ready: &unready}}},
			}}},
			wantErr: true,
		},
		{
			name: "IPv4 slice after ready IPv6 endpoint",
			slices: discoveryv1.EndpointSliceList{Items: []discoveryv1.EndpointSlice{
				{
					AddressType: discoveryv1.AddressTypeIPv6,
					Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"2001:db8::1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
				},
				{
					AddressType: discoveryv1.AddressTypeIPv4,
					Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"192.0.2.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
				},
			}},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := json.Marshal(test.slices)
			if err != nil {
				t.Fatal(err)
			}
			err = validateRemoteDcgmIPv6Endpoints(output)
			if test.wantErr && err == nil {
				t.Fatal("validateRemoteDcgmIPv6Endpoints() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateRemoteDcgmIPv6Endpoints() error = %v", err)
			}
		})
	}
}
