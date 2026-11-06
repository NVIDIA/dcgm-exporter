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

package cluster

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// TestHelmEnv separates runs and keeps explicit and inherited kubeconfig selection.
func TestHelmEnv(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name, runName, external, local, wantKubeconfig string
	}{
		{name: "default"},
		{name: "local", runName: "local-run", local: "/local/kubeconfig", wantKubeconfig: "/local/kubeconfig"},
		{name: "external", runName: "external-run", external: "/external/kubeconfig", local: "/local/kubeconfig", wantKubeconfig: "/external/kubeconfig"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Root: root, RunName: test.runName, Kubeconfig: test.external, LocalKubeconfig: test.local}
			state := filepath.Join(root, ".local-e2e", "helm-"+cfg.runName())
			want := []string{
				"HELM_REPOSITORY_CONFIG=" + filepath.Join(state, "repositories.yaml"),
				"HELM_REPOSITORY_CACHE=" + filepath.Join(state, "cache", "repository"),
				"HELM_CACHE_HOME=" + filepath.Join(state, "cache"),
				"HELM_CONTENT_CACHE=" + filepath.Join(state, "cache", "content"),
			}
			if test.wantKubeconfig != "" {
				want = append([]string{"KUBECONFIG=" + test.wantKubeconfig}, want...)
			}
			if got := helmEnv(cfg); !slices.Equal(got, want) {
				t.Fatalf("helmEnv() = %q, want %q", got, want)
			}
		})
	}
}

// TestHelmRepositoryIsolation runs real Helm against a local repository and
// checks that caller repository metadata, cache and registry auth stay intact.
func TestHelmRepositoryIsolation(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("real Helm executable is unavailable")
	}
	shared := t.TempDir()
	t.Setenv("HELM_KUBECONTEXT", "caller-context")
	for name, relative := range map[string]string{
		"HELM_CONFIG_HOME":       "config",
		"HELM_DATA_HOME":         "data",
		"HELM_CACHE_HOME":        "cache",
		"HELM_REPOSITORY_CONFIG": "config/repositories.yaml",
		"HELM_REPOSITORY_CACHE":  "cache/repository",
		"HELM_CONTENT_CACHE":     "cache/content",
		"HELM_REGISTRY_CONFIG":   "config/registry/config.json",
	} {
		t.Setenv(name, filepath.Join(shared, relative))
	}
	files := map[string]string{
		"config/repositories.yaml":    "apiVersion: v1\nrepositories:\n- name: nvidia\n  url: https://shared.invalid\n",
		"config/registry/config.json": "{\"auths\":{}}\n",
		"cache/repository/sentinel":   "shared repository cache\n",
		"cache/content/sentinel":      "shared content cache\n",
		"data/sentinel":               "shared data\n",
	}
	writeHelmTestFiles(t, shared, files)
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "apiVersion: v1\nentries: {}\n")
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	cfg := Config{Root: t.TempDir(), RunName: "isolation", Kubeconfig: "/caller/kubeconfig"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	execHelm := func(args ...string) []byte {
		t.Helper()
		var output bytes.Buffer
		if err := runHelm(ctx, e2eexec.OSRunner{}, &output, cfg, "helm_isolation", args); err != nil {
			t.Fatalf("helm %v failed: %v", args, err)
		}
		return output.Bytes()
	}
	environment := string(execHelm("env"))
	assertHelmTestEnvironment(t, environment, shared, helmStateDir(cfg))
	t.Logf("Helm version: %s", strings.TrimSpace(string(execHelm("version", "--short"))))
	repositories := []string{"nvidia-device-plugin", "nvidia", "nvidia-gpu-operator"}
	for _, name := range repositories {
		execHelm("repo", "add", name, server.URL, "--force-update")
	}
	execHelm("repo", "update")
	list := string(execHelm("repo", "list"))
	cache := filepath.Join(cfg.Root, ".local-e2e", "helm-isolation", "cache", "repository")
	assertHelmRepositories(t, list, cache, repositories)
	// Render a local chart through the same environment used for deployments.
	chart := filepath.Join(t.TempDir(), "chart")
	writeHelmTestFiles(t, chart, map[string]string{
		"Chart.yaml":            "apiVersion: v2\nname: isolation\nversion: 0.1.0\n",
		"templates/config.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\n",
	})
	if rendered := string(execHelm("template", "isolation", chart)); !strings.Contains(rendered, "name: isolation") {
		t.Fatal("Helm did not render the local chart")
	}
	execHelm("repo", "remove", "nvidia")
	if got := readHelmTestFiles(t, shared); !maps.Equal(got, files) {
		t.Fatalf("shared Helm files changed: got %q, want %q", got, files)
	}
}

// TestRunHelmRejectsInvalidRunName prevents path traversal before starting Helm.
func TestRunHelmRejectsInvalidRunName(t *testing.T) {
	for _, name := range []string{"../other", "a/../../other", "/absolute", "UPPER"} {
		runner := &recordingRunner{}
		cfg := Config{Root: t.TempDir(), RunName: name}
		if err := runHelm(context.Background(), runner, io.Discard, cfg, "helm_test", []string{"repo", "list"}); err == nil {
			t.Errorf("runHelm accepted invalid run name %q", name)
		}
		if len(runner.commands) != 0 {
			t.Fatalf("started a command with invalid run name %q", name)
		}
		if err := cleanupHelmState(cfg); err == nil {
			t.Errorf("cleanup accepted invalid run name %q", name)
		}
	}
}

// TestCleanupRemovesOnlyOwnedHelmState covers local and external cluster cleanup,
// including failures that must still remove local metadata.
func TestCleanupRemovesOnlyOwnedHelmState(t *testing.T) {
	base, err := ConfigForRunName("owned")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, kubeconfig string
		fail             bool
	}{
		{name: "external=false/fail=false"},
		{name: "external=false/fail=true", fail: true},
		{name: "external=true/fail=false", kubeconfig: "/caller/kubeconfig"},
		{name: "external=true/fail=true", kubeconfig: "/caller/kubeconfig", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			cfg.Root = t.TempDir()
			cfg.Kubeconfig = test.kubeconfig
			other := filepath.Join(".local-e2e", "helm-other")
			writeHelmTestFiles(t, cfg.Root, map[string]string{
				filepath.Join(".local-e2e", "helm-owned", "repositories.yaml"): "sentinel",
				filepath.Join(other, "repositories.yaml"):                      "sentinel",
			})
			runner := &recordingRunner{results: map[string]e2eexec.Result{
				"kubectl get namespace owned --ignore-not-found=true -o json":      {},
				"kubectl get namespace owned-dcgm --ignore-not-found=true -o json": {},
			}}
			if test.fail {
				runner.results["k3d cluster list"] = e2eexec.Result{ExitCode: 1}
				runner.results["kubectl get namespace owned --ignore-not-found=true -o json"] = e2eexec.Result{ExitCode: 1}
			}
			err := Cleanup(context.Background(), runner, io.Discard, cfg)
			if (err != nil) != test.fail {
				t.Fatalf("cleanup error = %v, want failure = %t", err, test.fail)
			}
			if _, err := os.Stat(helmStateDir(cfg)); !os.IsNotExist(err) {
				t.Fatalf("owned Helm state remains: %v", err)
			}
			want := map[string]string{"repositories.yaml": "sentinel"}
			if got := readHelmTestFiles(t, filepath.Join(cfg.Root, other)); !maps.Equal(got, want) {
				t.Fatalf("other run's state changed: got %q, want %q", got, want)
			}
		})
	}
}

// TestHelmStateRejectsMissingRoots requires explicit absolute ownership roots.
func TestHelmStateRejectsMissingRoots(t *testing.T) {
	for _, root := range []string{"", ".", "relative/root"} {
		if err := validateHelmState(Config{Root: root}); err == nil {
			t.Errorf("accepted non-absolute root %q", root)
		}
	}
}

// TestHelmStateRejectsSymlinks prevents shared state redirection.
func TestHelmStateRejectsSymlinks(t *testing.T) {
	for _, relative := range []string{".local-e2e", ".local-e2e/helm-dcgm-exporter", ".local-e2e/helm-dcgm-exporter/cache", ".local-e2e/helm-dcgm-exporter/repositories.yaml"} {
		t.Run(relative, func(t *testing.T) {
			cfg, shared := helmSymlinkFixture(t, relative)
			runner := &recordingRunner{}
			if err := runHelm(context.Background(), runner, io.Discard, cfg, "helm_test", []string{"repo", "list"}); err == nil || len(runner.commands) != 0 {
				t.Fatal("Helm ran through a symbolic link")
			}
			if err := cleanupHelmState(cfg); err == nil {
				t.Fatal("cleanup accepted a symbolic link")
			}
			want := map[string]string{"sentinel": "preserved"}
			if got := readHelmTestFiles(t, shared); !maps.Equal(got, want) {
				t.Fatalf("shared sentinel changed: got %q, want %q", got, want)
			}
		})
	}
}

// assertHelmTestEnvironment checks caller auth paths and run-owned metadata paths.
func assertHelmTestEnvironment(t *testing.T, environment, shared, state string) {
	t.Helper()
	for _, entry := range []string{
		`HELM_REGISTRY_CONFIG="` + filepath.Join(shared, "config/registry/config.json") + `"`,
		`HELM_CONFIG_HOME="` + filepath.Join(shared, "config") + `"`,
		`HELM_DATA_HOME="` + filepath.Join(shared, "data") + `"`,
		`HELM_KUBECONTEXT="caller-context"`,
		`HELM_REPOSITORY_CONFIG="` + filepath.Join(state, "repositories.yaml") + `"`,
		`HELM_CONTENT_CACHE="` + filepath.Join(state, "cache/content") + `"`,
	} {
		if !strings.Contains(environment, entry) {
			t.Errorf("Helm environment does not contain expected entry %s", entry)
		}
	}
}

// assertHelmRepositories checks that every repository is listed and has a cached index.
func assertHelmRepositories(t *testing.T, list, cache string, names []string) {
	t.Helper()
	for _, name := range names {
		_, err := os.Stat(filepath.Join(cache, name+"-index.yaml"))
		if !strings.Contains(list, name) || err != nil {
			t.Fatalf("isolated repository %s missing from list or index cache: %v", name, err)
		}
	}
}

// writeHelmTestFiles creates private filesystem fixtures beneath a test root.
func writeHelmTestFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for relative, content := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// readHelmTestFiles records every relative filename and its contents for comparison.
func readHelmTestFiles(t *testing.T, directory string) map[string]string {
	t.Helper()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files := make(map[string]string)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := root.ReadFile(name)
		if err != nil {
			return err
		}
		files[name] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// helmSymlinkFixture redirects one owned state path toward a shared sentinel.
func helmSymlinkFixture(t *testing.T, relative string) (Config, string) {
	t.Helper()
	cfg := Config{Root: t.TempDir()}
	shared := t.TempDir()
	writeHelmTestFiles(t, shared, map[string]string{"sentinel": "preserved"})
	link := filepath.Join(cfg.Root, relative)
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}
	return cfg, shared
}
