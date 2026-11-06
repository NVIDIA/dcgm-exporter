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
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const unitTestRunID = "unit-test-run"

// TestDebugDumpReaderDirectory checks that file operations stay inside the
// current run's directory.
func TestDebugDumpReaderDirectory(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		directory string
		want      string
		wantErr   bool
	}{
		{
			name:      "current run directory",
			directory: debugDumpDirectoryForRun(unitTestRunID),
			want:      filepath.Join(debugDumpReaderMount, unitTestRunID),
		},
		{
			name:      "shared root",
			directory: debugDumpRootDirectory,
			wantErr:   true,
		},
		{
			name:      "another run directory",
			directory: filepath.Join(debugDumpRootDirectory, "another-run"),
			wantErr:   true,
		},
		{
			name:      "outside shared root",
			directory: "/tmp/not-a-debug-dump",
			wantErr:   true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := debugDumpReaderDirectory(testCase.directory, unitTestRunID)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("expected path validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("debugDumpReaderDirectory() error = %v", err)
			}
			if got != testCase.want {
				t.Errorf("debugDumpReaderDirectory() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestSingleNodeName checks that host-path tests select one named node.
func TestSingleNodeName(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		nodes   []corev1.Node
		want    string
		wantErr bool
	}{
		{
			name:  "one node",
			nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "gpu-node"}}},
			want:  "gpu-node",
		},
		{
			name:    "no nodes",
			wantErr: true,
		},
		{
			name: "multiple nodes",
			nodes: []corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "gpu-node-a"}},
				{ObjectMeta: metav1.ObjectMeta{Name: "gpu-node-b"}},
			},
			wantErr: true,
		},
		{
			name:    "unnamed node",
			nodes:   []corev1.Node{{}},
			wantErr: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := singleNodeName(testCase.nodes)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("expected node selection error")
				}
				return
			}
			if err != nil {
				t.Fatalf("singleNodeName() error = %v", err)
			}
			if got != testCase.want {
				t.Errorf("singleNodeName() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestLocalPortForwardURLUsesIPv4Loopback keeps local scrape URLs on IPv4 loopback.
func TestLocalPortForwardURLUsesIPv4Loopback(t *testing.T) {
	if got := localPortForwardURL("https", 9400, "/metrics"); got != "https://127.0.0.1:9400/metrics" {
		t.Fatalf("localPortForwardURL() = %q", got)
	}
}

// TestDefaultExporterArgumentsHelmList checks the argument list the -arguments
// flag uses when the e2e CLI does not pass one.
func TestDefaultExporterArgumentsHelmList(t *testing.T) {
	want := "{-c=1000}"

	if got := helmArgumentList(defaultExporterArguments()); got != want {
		t.Fatalf("helmArgumentList(defaultExporterArguments()) = %q, want %q", got, want)
	}
}

// TestHelmArgumentListKeepsCommasInOneArgument checks that a selector with two
// members reaches Helm as one list item: its comma is escaped, and the commas
// between arguments are not.
func TestHelmArgumentListKeepsCommasInOneArgument(t *testing.T) {
	args := []string{"-c=1000", "--switch-devices=g:1+i:71,72", "--cpu-devices=g:0+i:3,4"}
	want := `{-c=1000,--switch-devices=g:1+i:71\,72,--cpu-devices=g:0+i:3\,4}`

	if got := helmArgumentList(args); got != want {
		t.Fatalf("helmArgumentList(%q) = %q, want %q", args, got, want)
	}
}
