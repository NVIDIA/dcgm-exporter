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
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// fieldDumpMaxFileBytes is the origin-tracking name of the per-file dump limit.
const fieldDumpMaxFileBytes = "DumpConfig.MaxFileBytes"

// fieldDumpRetention and keyDumpRetention are the origin-tracking name and the
// config file key of the dump retention.
const (
	fieldDumpRetention = "DumpConfig.Retention"
	keyDumpRetention   = "debug_dump.retention"
)

// maxDumpRetentionHours is the longest --dump-retention, in hours, that fits a
// time.Duration.
const maxDumpRetentionHours = int(math.MaxInt64 / int64(time.Hour))

// YAMLDebugDump configures file-based debug dumps.
type YAMLDebugDump struct {
	Enabled   *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Directory string `yaml:"directory" json:"directory"`
	// Retention is a duration of zero or more, such as 24h or 90m; 0 turns age
	// cleanup off.
	Retention     string `yaml:"retention,omitempty" json:"retention,omitempty"`
	Compression   *bool  `yaml:"compression,omitempty" json:"compression,omitempty"`
	MinInterval   string `yaml:"min_interval,omitempty" json:"min_interval,omitempty"`
	MaxFileBytes  *int64 `yaml:"max_file_bytes,omitempty" json:"max_file_bytes,omitempty"`
	MaxTotalBytes *int64 `yaml:"max_total_bytes,omitempty" json:"max_total_bytes,omitempty"`
	MaxFiles      *int64 `yaml:"max_files,omitempty" json:"max_files,omitempty"`
}

// DefaultDumpConfig returns the dump policy used when nothing sets it: dumps
// off, with finite storage limits that apply even when age cleanup is off.
func DefaultDumpConfig() DumpConfig {
	return DumpConfig{
		Directory:     "/tmp/dcgm-exporter-debug",
		Retention:     24 * time.Hour,
		Compression:   true,
		MinInterval:   time.Minute,
		MaxFileBytes:  16 << 20,
		MaxTotalBytes: 256 << 20,
		MaxFiles:      128,
	}
}

// DumpRetentionFromHours converts a --dump-retention value in hours to a
// retention. A negative value means no age cleanup, the same as 0, so it maps
// to 0. A value too large for a time.Duration is an error naming the flag.
func DumpRetentionFromHours(hours int) (time.Duration, error) {
	if hours > maxDumpRetentionHours {
		return 0, fmt.Errorf("invalid %s parameter value: %d, must be at most %d hours",
			FlagDumpRetention, hours, maxDumpRetentionHours)
	}

	return time.Duration(max(hours, 0)) * time.Hour, nil
}

// Validate reports every dump setting the file dumper cannot use.
func (c DumpConfig) Validate() error {
	var errs []error
	for _, err := range c.settingErrors() {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// settingErrors checks the dump settings, naming the field at fault in each error.
func (c DumpConfig) settingErrors() []*SettingError {
	var errs []*SettingError
	add := func(field string, format string, args ...any) {
		errs = append(errs, &SettingError{Field: field, Err: fmt.Errorf(format, args...)})
	}

	if c.Enabled && strings.TrimSpace(c.Directory) == "" {
		add("DumpConfig.Directory", "%s must not be empty when debug dumps are enabled", FlagDumpDirectory)
	}

	if c.MinInterval <= 0 {
		add("DumpConfig.MinInterval", "invalid %s parameter value: %q, must be greater than 0",
			FlagDumpMinInterval, c.MinInterval.String())
	}

	for _, limit := range []struct {
		flag, field string
		value       int64
	}{
		{FlagDumpMaxFileBytes, fieldDumpMaxFileBytes, c.MaxFileBytes},
		{FlagDumpMaxTotalBytes, "DumpConfig.MaxTotalBytes", c.MaxTotalBytes},
		{FlagDumpMaxFiles, "DumpConfig.MaxFiles", c.MaxFiles},
	} {
		if limit.value <= 0 {
			add(limit.field, "invalid %s parameter value: %d, must be greater than 0", limit.flag, limit.value)
		}
	}

	if c.MaxFileBytes > 0 && c.MaxTotalBytes > 0 && c.MaxFileBytes > c.MaxTotalBytes {
		add(fieldDumpMaxFileBytes, "invalid %s parameter value: %d, must not exceed %s (%d)",
			FlagDumpMaxFileBytes, c.MaxFileBytes, FlagDumpMaxTotalBytes, c.MaxTotalBytes)
	}

	return errs
}

// applyDebugDump applies debug_dump. Only the values the file sets are written.
func (y *YAMLConfig) applyDebugDump(config *Config, rec originRecorder) error {
	dump := y.DebugDump
	if dump == nil {
		return nil
	}

	if dump.Enabled != nil {
		config.DumpConfig.Enabled = *dump.Enabled
		rec.record("DumpConfig.Enabled", "debug_dump.enabled")
	}

	if directory := strings.TrimSpace(dump.Directory); directory != "" {
		config.DumpConfig.Directory = directory
		rec.record("DumpConfig.Directory", "debug_dump.directory")
	}

	if err := dump.applyRetention(config, rec); err != nil {
		return err
	}

	if dump.Compression != nil {
		config.DumpConfig.Compression = *dump.Compression
		rec.record("DumpConfig.Compression", "debug_dump.compression")
	}

	if interval := strings.TrimSpace(dump.MinInterval); interval != "" {
		duration, err := parsePositiveDuration(interval, "debug_dump.min_interval")
		if err != nil {
			return err
		}

		config.DumpConfig.MinInterval = duration
		rec.record("DumpConfig.MinInterval", "debug_dump.min_interval")
	}

	applyDumpLimit(dump.MaxFileBytes, &config.DumpConfig.MaxFileBytes, rec, fieldDumpMaxFileBytes, "debug_dump.max_file_bytes")
	applyDumpLimit(dump.MaxTotalBytes, &config.DumpConfig.MaxTotalBytes, rec, "DumpConfig.MaxTotalBytes", "debug_dump.max_total_bytes")
	applyDumpLimit(dump.MaxFiles, &config.DumpConfig.MaxFiles, rec, "DumpConfig.MaxFiles", "debug_dump.max_files")
	return nil
}

// applyRetention applies debug_dump.retention: a duration of zero or more,
// where 0 turns age cleanup off. An empty value changes nothing.
func (d *YAMLDebugDump) applyRetention(config *Config, rec originRecorder) error {
	retention := strings.TrimSpace(d.Retention)
	if retention == "" {
		return nil
	}

	duration, err := time.ParseDuration(retention)
	if err != nil {
		return keyWrap(keyDumpRetention, err)
	}

	if duration < 0 {
		return keyErrorf(keyDumpRetention, "must be 0 or more, such as 24h; 0 turns age cleanup off")
	}

	config.DumpConfig.Retention = duration
	rec.record(fieldDumpRetention, keyDumpRetention)
	return nil
}

// applyDumpLimit copies one storage limit the file sets and records its origin.
func applyDumpLimit(value *int64, target *int64, rec originRecorder, field, key string) {
	if value == nil {
		return
	}

	*target = *value
	rec.record(field, key)
}
