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

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	runtimeCategoryPackage   = "package"
	runtimeCategoryProject   = "project"
	runtimeCategoryGenerated = "generated"
	dcgmPackagePrefix        = "datacenter-gpu-manager-4"
)

var commonLicenseReference = regexp.MustCompile(`/usr/share/common-licenses/[A-Za-z0-9.+_-]+`)

// containerNoticeConfig identifies every artifact input needed to assemble one
// architecture-specific distroless notice and the package database used to resolve it.
type containerNoticeConfig struct {
	Architecture    string
	GoNoticesPath   string
	DCGMNoticesPath string
	RuntimeManifest string
	HelperRoot      string
	Packages        packageDatabase
}

// runtimeManifestEntry records why one helper-stage file is shipped and maps
// its original package-owned path to its final image path.
type runtimeManifestEntry struct {
	Category    string
	Source      string
	Destination string
}

// packageMetadata is the binary and source package identity printed beside a
// package legal document in the generated notice.
type packageMetadata struct {
	BinaryPackage string
	BinaryVersion string
	SourcePackage string
	SourceVersion string
}

// packageDatabase resolves helper-stage file ownership and retrieves the exact
// installed package versions used by the image build.
type packageDatabase interface {
	Owner(path string) (string, error)
	Metadata(binaryPackage string) (packageMetadata, error)
}

// dpkgPackageDatabase implements packageDatabase by invoking the helper
// image's dpkg-query executable without a shell.
type dpkgPackageDatabase struct{ Command string }

// noticeComponent associates one legal document with a package.
type noticeComponent struct {
	BinaryPackage string
	BinaryVersion string
}

// key returns the stable package identity used to merge repeated references.
func (c noticeComponent) key() string {
	return c.BinaryPackage + "\x00" + c.BinaryVersion
}

// legalDocument holds one content-deduplicated legal document and every
// package and logical legal-file path associated with that text.
type legalDocument struct {
	Hash       string
	Text       []byte
	LegalPaths map[string]struct{}
	Components map[string]*noticeComponent
}

// commonLicenseDocument holds one content-deduplicated common-license text.
type commonLicenseDocument struct {
	Hash  string
	Text  []byte
	Paths map[string]struct{}
}

// noticeCollection accumulates package legal documents and their referenced
// common licenses before deterministic rendering.
type noticeCollection struct {
	LegalDocuments map[string]*legalDocument
	CommonLicenses map[string]*commonLicenseDocument
}

// assembleContainerNotices validates the staged runtime inventory, collects
// helper package legal material, and renders the artifact-specific notice.
func assembleContainerNotices(config containerNoticeConfig) ([]byte, error) {
	if err := validateContainerNoticeConfig(config); err != nil {
		return nil, err
	}

	goNotices, err := os.ReadFile(config.GoNoticesPath)
	if err != nil {
		return nil, fmt.Errorf("read Go notices: %w", err)
	}
	if len(bytes.TrimSpace(goNotices)) == 0 {
		return nil, errors.New("go notices are empty")
	}
	dcgmNotices, err := os.ReadFile(config.DCGMNoticesPath)
	if err != nil {
		return nil, fmt.Errorf("read DCGM notices: %w", err)
	}
	if len(bytes.TrimSpace(dcgmNotices)) == 0 {
		return nil, errors.New("DCGM notices are empty")
	}

	manifest, err := readRuntimeManifest(config.RuntimeManifest)
	if err != nil {
		return nil, err
	}

	collection := noticeCollection{
		LegalDocuments: make(map[string]*legalDocument),
		CommonLicenses: make(map[string]*commonLicenseDocument),
	}

	packages := make(map[string]packageMetadata)
	dcgmPackageFound := false
	for _, entry := range manifest {
		if entry.Category != runtimeCategoryPackage {
			continue
		}
		owner, err := config.Packages.Owner(entry.Source)
		if err != nil {
			return nil, fmt.Errorf("resolve owner for %s shipped as %s: %w", entry.Source, entry.Destination, err)
		}
		metadata, found := packages[owner]
		if !found {
			metadata, err = config.Packages.Metadata(owner)
			if err != nil {
				return nil, fmt.Errorf("read metadata for %s: %w", owner, err)
			}
			packages[owner] = metadata
		}
		if strings.HasPrefix(metadata.BinaryPackage, dcgmPackagePrefix) {
			dcgmPackageFound = true
			continue
		}
		if err := collection.addPackage(config.HelperRoot, metadata); err != nil {
			return nil, err
		}
	}
	if !dcgmPackageFound {
		return nil, errors.New("runtime manifest contains no DCGM package-owned files")
	}
	return renderContainerNotices(config.Architecture, goNotices, dcgmNotices, collection), nil
}

// validateContainerNoticeConfig rejects an incomplete container-mode request
// before any artifact inputs are read.
func validateContainerNoticeConfig(config containerNoticeConfig) error {
	for name, value := range map[string]string{
		"architecture":      config.Architecture,
		"Go notices path":   config.GoNoticesPath,
		"DCGM notices path": config.DCGMNoticesPath,
		"runtime manifest":  config.RuntimeManifest,
		"helper root":       config.HelperRoot,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required in container mode", name)
		}
	}
	if config.Packages == nil {
		return errors.New("package database is required in container mode")
	}
	return nil
}

// readRuntimeManifest parses and validates the category, source, and
// destination recorded for every helper-derived runtime entry.
func readRuntimeManifest(path string) ([]runtimeManifestEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open runtime manifest: %w", err)
	}
	defer file.Close()

	var entries []runtimeManifestEntry
	seenDestinations := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("runtime manifest line %d: expected category, source, and destination", lineNumber)
		}
		entry := runtimeManifestEntry{Category: fields[0], Source: fields[1], Destination: fields[2]}
		switch entry.Category {
		case runtimeCategoryPackage, runtimeCategoryProject, runtimeCategoryGenerated:
		default:
			return nil, fmt.Errorf("runtime manifest line %d: unsupported category %q", lineNumber, entry.Category)
		}
		if err := validateAbsoluteManifestPath("source", entry.Source); err != nil {
			return nil, fmt.Errorf("runtime manifest line %d: %w", lineNumber, err)
		}
		if err := validateAbsoluteManifestPath("destination", entry.Destination); err != nil {
			return nil, fmt.Errorf("runtime manifest line %d: %w", lineNumber, err)
		}
		if _, exists := seenDestinations[entry.Destination]; exists {
			return nil, fmt.Errorf("runtime manifest line %d: duplicate destination %s", lineNumber, entry.Destination)
		}
		seenDestinations[entry.Destination] = struct{}{}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read runtime manifest: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("runtime manifest is empty")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Destination < entries[j].Destination })
	return entries, nil
}

// validateAbsoluteManifestPath ensures a manifest path is canonical, absolute,
// and safe to represent as a single tab-separated field.
func validateAbsoluteManifestPath(kind, path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\t\r\n") {
		return fmt.Errorf("invalid %s path %q", kind, path)
	}
	return nil
}

// addPackage loads a package's copyright file and common-license references.
func (collection noticeCollection) addPackage(root string, metadata packageMetadata) error {
	if err := validatePackageMetadata(metadata); err != nil {
		return fmt.Errorf("package metadata: %w", err)
	}
	documentPackage := metadata.BinaryPackage
	if name, _, found := strings.Cut(documentPackage, ":"); found {
		documentPackage = name
	}
	logicalPath := filepath.ToSlash(filepath.Join("/usr/share/doc", documentPackage, "copyright"))
	document, contents, err := collection.addLegalFile(root, logicalPath)
	if err != nil {
		return fmt.Errorf("read legal file for %s %s at %s: %w", metadata.BinaryPackage, metadata.BinaryVersion, logicalPath, err)
	}
	component := noticeComponent{
		BinaryPackage: metadata.BinaryPackage,
		BinaryVersion: metadata.BinaryVersion,
	}
	componentKey := component.key()
	document.Components[componentKey] = &component

	for _, reference := range findCommonLicenseReferences(contents) {
		if err := collection.addCommonLicense(root, reference); err != nil {
			return err
		}
	}
	return nil
}

// addLegalFile reads, validates, and content-deduplicates one legal document.
func (collection noticeCollection) addLegalFile(root, logicalPath string) (*legalDocument, []byte, error) {
	contents, err := readRootedFile(root, logicalPath)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return nil, nil, fmt.Errorf("legal file %s is empty", logicalPath)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(contents))
	document := collection.LegalDocuments[hash]
	if document == nil {
		document = &legalDocument{
			Hash:       hash,
			Text:       contents,
			LegalPaths: make(map[string]struct{}),
			Components: make(map[string]*noticeComponent),
		}
		collection.LegalDocuments[hash] = document
	}
	document.LegalPaths[logicalPath] = struct{}{}
	return document, contents, nil
}

// findCommonLicenseReferences returns the stable set of Debian common-license
// paths mentioned by an authoritative package legal document.
func findCommonLicenseReferences(contents []byte) []string {
	matches := commonLicenseReference.FindAllString(string(contents), -1)
	for index := range matches {
		matches[index] = strings.TrimRight(matches[index], ".,;:)")
	}
	return sortedUniqueStrings(matches)
}

// validatePackageMetadata ensures every package identity field is present and
// safe to render as a single line in the generated notice.
func validatePackageMetadata(metadata packageMetadata) error {
	for name, value := range map[string]string{
		"binary package": metadata.BinaryPackage,
		"binary version": metadata.BinaryVersion,
		"source package": metadata.SourcePackage,
		"source version": metadata.SourceVersion,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\t\r\n") {
			return fmt.Errorf("invalid %s %q", name, value)
		}
	}
	return nil
}

// addCommonLicense loads and content-deduplicates a referenced Debian common license.
func (collection noticeCollection) addCommonLicense(root, logicalPath string) error {
	contents, err := readRootedFile(root, logicalPath)
	if err != nil {
		return fmt.Errorf("read referenced common license %s: %w", logicalPath, err)
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return fmt.Errorf("referenced common license %s is empty", logicalPath)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(contents))
	document := collection.CommonLicenses[hash]
	if document == nil {
		document = &commonLicenseDocument{
			Hash:  hash,
			Text:  contents,
			Paths: make(map[string]struct{}),
		}
		collection.CommonLicenses[hash] = document
	}
	document.Paths[logicalPath] = struct{}{}
	return nil
}

// rootedPath maps an absolute in-image path beneath a staged filesystem root
// and rejects lexical attempts to escape that root.
func rootedPath(root, logicalPath string) (string, error) {
	if !filepath.IsAbs(logicalPath) {
		return "", fmt.Errorf("path %q is not absolute", logicalPath)
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(logicalPath)
	joined := filepath.Join(rootAbsolute, strings.TrimPrefix(clean, string(filepath.Separator)))
	relative, err := filepath.Rel(rootAbsolute, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes root %s", logicalPath, root)
	}
	return joined, nil
}

// readRootedFile follows an in-root legal-file symlink, verifies the resolved
// target remains beneath the staged root, and returns its contents.
func readRootedFile(root, logicalPath string) ([]byte, error) {
	path, err := rootedPath(root, logicalPath)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(rootAbsolute, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("resolved path %s escapes root %s", logicalPath, root)
	}
	return os.ReadFile(resolved)
}

// renderContainerNotices combines the generated Go notice, the notice supplied
// by the installed DCGM package, and notices for additional runtime packages.
func renderContainerNotices(
	architecture string,
	goNotices []byte,
	dcgmNotices []byte,
	collection noticeCollection,
) []byte {
	var output bytes.Buffer
	fmt.Fprintln(&output, "THIRD-PARTY NOTICES FOR THE NVIDIA DCGM EXPORTER DISTROLESS IMAGE")
	fmt.Fprintln(&output)
	fmt.Fprintf(&output, "Architecture: %s\n", architecture)
	fmt.Fprintln(&output, "The base image retains its own notices in their original locations.")
	fmt.Fprintln(&output)
	output.WriteString(separator)
	fmt.Fprintln(&output, "DCGM-EXPORTER BINARY NOTICES")
	output.WriteString(separator)
	fmt.Fprintln(&output)
	writeExactText(&output, goNotices)

	output.WriteByte('\n')
	output.WriteString(separator)
	fmt.Fprintln(&output, "DCGM PACKAGE THIRD-PARTY NOTICES")
	output.WriteString(separator)
	fmt.Fprintln(&output)
	writeExactText(&output, dcgmNotices)

	output.WriteByte('\n')
	output.WriteString(separator)
	fmt.Fprintln(&output, "THIRD-PARTY RUNTIME FILES ADDED TO THE DISTROLESS IMAGE")
	output.WriteString(separator)

	for _, document := range sortedLegalDocuments(collection.LegalDocuments) {
		output.WriteByte('\n')
		for _, component := range sortedComponents(document.Components) {
			fmt.Fprintf(&output, "Component: %s %s\n", component.BinaryPackage, component.BinaryVersion)
		}
		for _, path := range sortedSet(document.LegalPaths) {
			fmt.Fprintf(&output, "Legal file: %s\n", path)
		}
		fmt.Fprintln(&output)
		writeExactText(&output, document.Text)
	}

	output.WriteByte('\n')
	output.WriteString(separator)
	fmt.Fprintln(&output, "REFERENCED COMMON LICENSE TEXTS")
	output.WriteString(separator)
	for _, document := range sortedCommonLicenseDocuments(collection.CommonLicenses) {
		output.WriteByte('\n')
		for _, path := range sortedSet(document.Paths) {
			fmt.Fprintf(&output, "Common license: %s\n", path)
		}
		fmt.Fprintln(&output)
		writeExactText(&output, document.Text)
	}
	return output.Bytes()
}

// writeExactText preserves the referenced bytes and adds only a separating
// newline when the next generated field would otherwise touch the final byte.
func writeExactText(output *bytes.Buffer, contents []byte) {
	output.Write(contents)
	if len(contents) == 0 || contents[len(contents)-1] != '\n' {
		output.WriteByte('\n')
	}
}

// sortedLegalDocuments orders attributed documents by component and supplied
// third-party notices by path, with the content hash as a final tie-breaker.
func sortedLegalDocuments(documents map[string]*legalDocument) []*legalDocument {
	result := make([]*legalDocument, 0, len(documents))
	for _, document := range documents {
		result = append(result, document)
	}
	sort.Slice(result, func(i, j int) bool {
		key := func(document *legalDocument) string {
			components := sortedComponents(document.Components)
			if len(components) > 0 {
				return "component\x00" + components[0].key()
			}
			return "notice\x00" + strings.Join(sortedSet(document.LegalPaths), "\x00")
		}
		left := key(result[i])
		right := key(result[j])
		if left == right {
			return result[i].Hash < result[j].Hash
		}
		return left < right
	})
	return result
}

// sortedCommonLicenseDocuments orders common-license texts by their logical
// paths and then by content hash.
func sortedCommonLicenseDocuments(documents map[string]*commonLicenseDocument) []*commonLicenseDocument {
	result := make([]*commonLicenseDocument, 0, len(documents))
	for _, document := range documents {
		result = append(result, document)
	}
	sort.Slice(result, func(i, j int) bool {
		left := strings.Join(sortedSet(result[i].Paths), "\x00")
		right := strings.Join(sortedSet(result[j].Paths), "\x00")
		if left == right {
			return result[i].Hash < result[j].Hash
		}
		return left < right
	})
	return result
}

// sortedComponents returns a legal document's package associations in stable
// component-identity order.
func sortedComponents(components map[string]*noticeComponent) []*noticeComponent {
	result := make([]*noticeComponent, 0, len(components))
	for _, component := range components {
		result = append(result, component)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key() < result[j].key() })
	return result
}

// sortedSet converts a string set to lexical order for deterministic output.
func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// sortedUniqueStrings deduplicates input strings and returns them in lexical order.
func sortedUniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return sortedSet(set)
}

// Owner returns the one Debian package that owns a helper path, falling back to
// the resolved target when the path is a compatibility symlink.
func (database dpkgPackageDatabase) Owner(path string) (string, error) {
	if database.Command == "" {
		return "", errors.New("dpkg-query command is empty")
	}
	candidates := []string{path}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
		candidates = append(candidates, resolved)
	}
	var failures []string
	for _, candidate := range candidates {
		// #nosec G204 -- dpkg-query is an explicitly configured build tool and
		// exec.Command passes the validated absolute package path without a shell.
		command := exec.Command(database.Command, "-S", candidate)
		command.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
		output, err := command.CombinedOutput()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", candidate, strings.TrimSpace(string(output))))
			continue
		}
		owner, err := parseDpkgOwner(candidate, output)
		if err != nil {
			return "", err
		}
		return owner, nil
	}
	return "", fmt.Errorf("no package owns %s (%s)", path, strings.Join(failures, "; "))
}

// parseDpkgOwner validates dpkg-query -S output and rejects missing or
// ambiguous ownership instead of guessing which package to attribute.
func parseDpkgOwner(path string, output []byte) (string, error) {
	var owners []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		suffix := ": " + path
		if !strings.HasSuffix(line, suffix) {
			continue
		}
		ownerList := strings.TrimSuffix(line, suffix)
		owners = append(owners, strings.Split(ownerList, ", ")...)
	}
	owners = sortedUniqueStrings(owners)
	if len(owners) != 1 || owners[0] == "" {
		return "", fmt.Errorf("dpkg-query returned %d owners for %s: %q", len(owners), path, strings.TrimSpace(string(output)))
	}
	return owners[0], nil
}

// Metadata retrieves the exact installed binary/source package names and
// versions used to label helper-derived runtime content.
func (database dpkgPackageDatabase) Metadata(binaryPackage string) (packageMetadata, error) {
	format := `${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n`
	// #nosec G204 -- dpkg-query is an explicitly configured build tool and
	// exec.Command passes the package name as a literal argument without a shell.
	command := exec.Command(database.Command, "-W", "-f="+format, binaryPackage)
	command.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	output, err := command.CombinedOutput()
	if err != nil {
		return packageMetadata{}, fmt.Errorf("dpkg-query -W %s: %w: %s", binaryPackage, err, strings.TrimSpace(string(output)))
	}
	fields := strings.Split(strings.TrimSuffix(string(output), "\n"), "\t")
	if len(fields) != 4 {
		return packageMetadata{}, fmt.Errorf("dpkg-query returned malformed metadata for %s: %q", binaryPackage, strings.TrimSpace(string(output)))
	}
	metadata := packageMetadata{
		BinaryPackage: fields[0],
		BinaryVersion: fields[1],
		SourcePackage: fields[2],
		SourceVersion: fields[3],
	}
	if err := validatePackageMetadata(metadata); err != nil {
		return packageMetadata{}, err
	}
	return metadata, nil
}
