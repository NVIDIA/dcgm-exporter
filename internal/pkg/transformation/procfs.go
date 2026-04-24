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

// Package transformation: procfs.go provides a minimal /proc reader
// abstraction (ProcFS) used by the bare-metal user mapper. The default
// implementation realProcFS reads from a configurable root (default "/proc")
// so tests can drive it against a fake filesystem laid out in t.TempDir().
//
// Feature 001-multi-user-gpu-util, task T014.

package transformation

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	sysOS "os"
	"path/filepath"
	"strconv"
)

// ProcFS is the minimal surface the bare-metal user mapper needs from /proc.
// The interface is narrow on purpose: we only need the UID from <pid>/status
// and a single environment variable from <pid>/environ.
type ProcFS interface {
	// ReadStatus returns the real UID of the process with the given PID,
	// parsed from /proc/<pid>/status (the first integer after "Uid:"). If the
	// status file is missing or unreadable, a non-nil error is returned and
	// the caller should treat the process as unresolvable for this cycle.
	ReadStatus(pid uint32) (uid uint32, err error)

	// ReadEnviron returns the value of the environment variable `key` set on
	// the process with the given PID, parsed from /proc/<pid>/environ
	// (NUL-separated KEY=VALUE tokens). When the key is not present, an empty
	// string is returned with a nil error, so callers can apply a fallback
	// (FR-004). When the environ file itself is missing/unreadable, a non-nil
	// error is returned so callers can distinguish "no value" from "no
	// permission / process gone".
	ReadEnviron(pid uint32, key string) (value string, err error)
}

// realProcFS is the production implementation, rooted at /proc by default.
// Tests instantiate it with a custom root pointing at a tmp dir.
type realProcFS struct {
	root string
}

// NewProcFS returns a ProcFS bound to the host /proc filesystem.
func NewProcFS() ProcFS { return &realProcFS{root: "/proc"} }

// NewProcFSAt returns a ProcFS bound to the given root (useful in tests).
func NewProcFSAt(root string) ProcFS { return &realProcFS{root: root} }

// ReadStatus reads <root>/<pid>/status and parses the Uid line.
func (p *realProcFS) ReadStatus(pid uint32) (uint32, error) {
	path := filepath.Join(p.root, strconv.FormatUint(uint64(pid), 10), "status")
	data, err := sysOS.ReadFile(path)
	if err != nil {
		return 0, err
	}
	// Look for a line beginning with "Uid:" (tab or spaces before the next fields).
	const prefix = "Uid:"
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		var line []byte
		if nl < 0 {
			line = data
			data = nil
		} else {
			line = data[:nl]
			data = data[nl+1:]
		}
		if !bytes.HasPrefix(line, []byte(prefix)) {
			continue
		}
		// Uid format: "Uid:\t<real>\t<effective>\t<saved>\t<fs>".
		fields := bytes.Fields(line[len(prefix):])
		if len(fields) < 1 {
			return 0, fmt.Errorf("procfs: malformed Uid line in %s", path)
		}
		uid, err := strconv.ParseUint(string(fields[0]), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("procfs: parse Uid %q in %s: %w", fields[0], path, err)
		}
		return uint32(uid), nil
	}
	return 0, errors.New("procfs: Uid line not found in " + path)
}

// ReadEnviron reads <root>/<pid>/environ and returns the value for `key`.
// Returns ("", nil) when the key is not present. I/O errors are returned verbatim.
func (p *realProcFS) ReadEnviron(pid uint32, key string) (string, error) {
	path := filepath.Join(p.root, strconv.FormatUint(uint64(pid), 10), "environ")
	f, err := sysOS.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// /proc/<pid>/environ is typically small (a few KB). Read the whole thing.
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	needle := []byte(key + "=")
	for len(data) > 0 {
		nul := bytes.IndexByte(data, 0x00)
		var token []byte
		if nul < 0 {
			token = data
			data = nil
		} else {
			token = data[:nul]
			data = data[nul+1:]
		}
		if bytes.HasPrefix(token, needle) {
			return string(token[len(needle):]), nil
		}
	}
	return "", nil
}
