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

package logging

import (
	"fmt"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/sirupsen/logrus"
)

// LogrusAdapter is an adapter that allows logrus Logger to be used as a go-kit/log Logger.
type LogrusAdapter struct {
	Logger *logrus.Logger
}

// NewLogrusAdapter creates a new LogrusAdapter with the provided logrus.Logger.
func NewLogrusAdapter(logger *logrus.Logger) log.Logger {
	return &LogrusAdapter{
		Logger: logger,
	}
}

// Log implements the go-kit/log Logger interface.
func (la *LogrusAdapter) Log(keyvals ...interface{}) error {
	// keyvals is a slice of interfaces, that represents a key-value pairs.
	if len(keyvals)%2 != 0 {
		keyvals = append(keyvals, "MISSING")
	}

	fields := logrus.Fields{}
	for i := 0; i < len(keyvals); i += 2 {
		key, ok := keyvals[i].(string)
		if !ok {
			// If the key is not la string, use la default key
			key = "missing_key"
		}
		fields[key] = keyvals[i+1]
	}

	// The go-kit/log uses msg field to keep log message, we don't want to use message as field in the logrus.
	msg, exists := fields["msg"]
	if exists {
		delete(fields, "msg")
	}

	// The go-kit/log uses level fields to keep log level. We need to convert this field into logrus value.
	lvl, exists := fields["level"]
	if !exists {
		fields["level"] = level.InfoValue()
	}
	delete(fields, "level")
	parsedLvl, err := logrus.ParseLevel(fmt.Sprint(lvl))
	if err != nil {
		parsedLvl = logrus.InfoLevel
	}

	la.Logger.WithFields(fields).Log(parsedLvl, msg)

	return nil
}
