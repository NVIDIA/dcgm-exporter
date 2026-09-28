// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command generate creates third-party notice bundles for the shipping binary
// and assembles those notices with package legal files for the distroless image.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const separator = "================================================================================\n"

// target identifies one shipping GOOS/GOARCH pair whose dependency closure is collected.
type target struct {
	GOOS   string
	GOARCH string
}

// String returns the GOOS/GOARCH form accepted by the command-line flag.
func (t target) String() string {
	return t.GOOS + "/" + t.GOARCH
}

// goListPackage contains the module metadata used from one `go list -json` package.
type goListPackage struct {
	Module *goListModule
}

// goListModule mirrors the subset of Go module metadata needed for notice collection.
type goListModule struct {
	Path    string
	Version string
	Dir     string
	Main    bool
	Replace *goListModule
}

// module identifies one immutable module-cache directory to inspect for legal files.
type module struct {
	Path    string
	Version string
	Dir     string
}

// key returns the path-and-version identity used to deduplicate modules across targets.
func (m module) key() string {
	return m.Path + "@" + m.Version
}

// noticeFile holds one top-level legal file from a dependency module.
type noticeFile struct {
	Name string
	Text []byte
}

// component groups a dependency module identity with its collected legal files.
type component struct {
	Path        string
	Version     string
	NoticeFiles []noticeFile
}

// main parses generator inputs, collects the target closures, and writes the notice source.
func main() {
	mode := flag.String("mode", "go", "generation mode: go or container")
	packagePath := flag.String("package", "./cmd/dcgm-exporter", "Go package path for the shipping binary")
	noticesPath := flag.String("notices", "THIRD_PARTY_NOTICES", "output path for the human-readable notice bundle")
	targetsFlag := flag.String("targets", "linux/amd64,linux/arm64", "comma-separated GOOS/GOARCH targets")
	goNoticesPath := flag.String("go-notices", "", "checked-in Go notice bundle used in container mode")
	dcgmNoticesPath := flag.String("dcgm-notices", "", "notice bundle supplied by the installed DCGM package")
	runtimeManifest := flag.String("runtime-manifest", "", "runtime provenance manifest")
	helperRoot := flag.String("helper-root", "/", "root containing helper package legal files")
	architecture := flag.String("architecture", "", "container target architecture")
	dpkgQuery := flag.String("dpkg-query", "dpkg-query", "dpkg-query executable used in container mode")
	flag.Parse()

	var notices []byte
	var err error
	switch *mode {
	case "go":
		var targets []target
		targets, err = parseTargets(*targetsFlag)
		if err == nil {
			var components []component
			components, err = collectComponents(*packagePath, targets)
			if err == nil {
				notices = renderNotices(*packagePath, targets, components)
			}
		}
	case "container":
		notices, err = assembleContainerNotices(containerNoticeConfig{
			Architecture:    *architecture,
			GoNoticesPath:   *goNoticesPath,
			DCGMNoticesPath: *dcgmNoticesPath,
			RuntimeManifest: *runtimeManifest,
			HelperRoot:      *helperRoot,
			Packages:        dpkgPackageDatabase{Command: *dpkgQuery},
		})
	default:
		err = fmt.Errorf("unsupported mode %q; expected go or container", *mode)
	}
	if err != nil {
		die(err)
	}

	if err := writeFile(*noticesPath, notices); err != nil {
		die(fmt.Errorf("write notices: %w", err))
	}
}

// die reports a generator error and exits with a nonzero status.
func die(err error) {
	fmt.Fprintln(os.Stderr, "third-party notices:", err)
	os.Exit(1)
}

// parseTargets validates, deduplicates, and sorts the requested GOOS/GOARCH pairs.
func parseTargets(value string) ([]target, error) {
	if value == "" {
		return nil, errors.New("at least one target is required")
	}

	var targets []target
	seen := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		parts := strings.Split(strings.TrimSpace(item), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid target %q; expected GOOS/GOARCH", item)
		}
		target := target{GOOS: parts[0], GOARCH: parts[1]}
		if _, ok := seen[target.String()]; ok {
			continue
		}
		seen[target.String()] = struct{}{}
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].String() < targets[j].String() })
	return targets, nil
}

// collectComponents unions target dependency closures and reads their verified legal files.
func collectComponents(packagePath string, targets []target) ([]component, error) {
	modules := make(map[string]module)
	for _, target := range targets {
		listed, err := listModules(packagePath, target)
		if err != nil {
			return nil, err
		}
		for _, listedModule := range listed {
			if listedModule.Path == "github.com/NVIDIA/go-dcgm" || listedModule.Path == "github.com/NVIDIA/go-nvml" {
				continue
			}
			modules[listedModule.key()] = listedModule
		}
	}

	if err := verifyModuleCache(); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(modules))
	for key := range modules {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	components := make([]component, 0, len(keys))
	for _, key := range keys {
		listedModule := modules[key]
		noticeFiles, err := readNoticeFiles(listedModule.Dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", listedModule.key(), err)
		}
		components = append(components, component{
			Path:        listedModule.Path,
			Version:     listedModule.Version,
			NoticeFiles: noticeFiles,
		})
	}
	return components, nil
}

// verifyModuleCache checks downloaded module contents against go.sum before they are read.
func verifyModuleCache() error {
	command := newGoCommand("mod", "verify")
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}

	details := strings.TrimSpace(string(output))
	if details == "" {
		return fmt.Errorf("verify module cache: %w", err)
	}
	return fmt.Errorf("verify module cache: %w: %s", err, details)
}

// listModules returns the non-main modules linked into packagePath for one target.
func listModules(packagePath string, target target) ([]module, error) {
	command := newGoCommand("list", "-buildvcs=false", "-deps", "-json", packagePath)
	command.Env = append(command.Env, "GOOS="+target.GOOS, "GOARCH="+target.GOARCH, "CGO_ENABLED=1")
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return nil, fmt.Errorf("list dependencies for %s: %w: %s", target, err, strings.TrimSpace(string(exitError.Stderr)))
		}
		return nil, fmt.Errorf("list dependencies for %s: %w", target, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	modules := make(map[string]module)
	for {
		var pkg goListPackage
		err := decoder.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode dependency metadata for %s: %w", target, err)
		}
		if pkg.Module == nil || pkg.Module.Main {
			continue
		}
		if pkg.Module.Replace != nil {
			return nil, fmt.Errorf("dependency %s uses a replace directive; add an approved collection path before generating notices", pkg.Module.Path)
		}
		if pkg.Module.Path == "" || pkg.Module.Version == "" || pkg.Module.Dir == "" {
			return nil, fmt.Errorf("dependency has incomplete module metadata for %s", target)
		}
		listedModule := module{Path: pkg.Module.Path, Version: pkg.Module.Version, Dir: pkg.Module.Dir}
		modules[listedModule.key()] = listedModule
	}

	result := make([]module, 0, len(modules))
	for _, listedModule := range modules {
		result = append(result, listedModule)
	}
	return result, nil
}

// newGoCommand creates a Go command that cannot inherit a developer or CI workspace.
func newGoCommand(arguments ...string) *exec.Cmd {
	command := exec.Command("go", arguments...)
	command.Env = append(os.Environ(), "GOWORK=off")
	return command
}

// readNoticeFiles returns the sorted top-level legal files from one verified module.
func readNoticeFiles(directory string) ([]noticeFile, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read module directory: %w", err)
	}

	var files []noticeFile
	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() || !isLegalFileName(entry.Name()) {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if len(bytes.TrimSpace(contents)) == 0 {
			return nil, fmt.Errorf("%s is empty", entry.Name())
		}
		files = append(files, noticeFile{Name: entry.Name(), Text: contents})
	}
	if len(files) == 0 {
		return nil, errors.New("no top-level LICENSE, NOTICE, COPYRIGHT, COPYING, PATENTS, or UNLICENSE file found")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// isLegalFileName reports whether a top-level filename is recognized as legal material.
func isLegalFileName(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"license", "licence", "notice", "copyright", "copying", "patents", "unlicense"} {
		if name == prefix || strings.HasPrefix(name, prefix+".") || strings.HasPrefix(name, prefix+"-") || strings.HasPrefix(name, prefix+"_") {
			return true
		}
	}
	return false
}

// renderNotices renders the deterministic human-readable module notice source.
func renderNotices(packagePath string, targets []target, components []component) []byte {
	var output bytes.Buffer
	fmt.Fprintln(&output, "THIRD-PARTY NOTICES FOR THE DCGM-EXPORTER BINARY")
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "This generated file contains the legal materials supplied by third-party Go modules compiled into the production dcgm-exporter binary for the targets below.")
	fmt.Fprintln(&output, "It is intended to ship beside the project LICENSE in standalone binary distributions.")
	fmt.Fprintln(&output, "Runtime libraries supplied by the host are not part of that binary payload and are outside this file's scope.")
	fmt.Fprintln(&output, "Container images combine this file with the legal materials for every additional component they ship. Do not edit this file manually.")
	fmt.Fprintf(&output, "Package: %s\n", packagePath)
	fmt.Fprintf(&output, "Targets: %s\n", strings.Join(targetNames(targets), ", "))
	fmt.Fprintln(&output, "Scope: third-party Go code compiled into the production dcgm-exporter binary.")
	fmt.Fprintln(&output, "NVIDIA-authored modules are excluded from this third-party notice bundle.")
	fmt.Fprintln(&output, "Excluded: dynamically supplied host libraries and other files not contained in the standalone binary payload.")
	fmt.Fprintln(&output)

	for componentIndex, component := range components {
		output.WriteString(separator)
		fmt.Fprintf(&output, "Module: %s\n", component.Path)
		fmt.Fprintf(&output, "Version: %s\n", component.Version)
		for _, noticeFile := range component.NoticeFiles {
			fmt.Fprintf(&output, "File: %s\n", noticeFile.Name)
		}
		output.WriteString(separator)
		for _, noticeFile := range component.NoticeFiles {
			fmt.Fprintf(&output, "\n--- %s ---\n\n", noticeFile.Name)
			output.Write(noticeFile.Text)
			if len(noticeFile.Text) == 0 || noticeFile.Text[len(noticeFile.Text)-1] != '\n' {
				output.WriteByte('\n')
			}
		}
		if componentIndex < len(components)-1 {
			output.WriteByte('\n')
		}
	}
	return output.Bytes()
}

// targetNames converts targets to their stable GOOS/GOARCH strings for the header.
func targetNames(targets []target) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.String())
	}
	return names
}

// writeFile creates the destination directory and writes one generated output.
func writeFile(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, contents, 0o600)
}
