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

package stdout

import (
	"strings"
	"time"
)

// outputEntry represents the structured form of the parsed log entry.
type outputEntry struct {
	Timestamp   time.Time
	Level       string
	Message     string
	IsRawString bool
}

// parseOutputEntry takes a log entry string and returns a structured outputEntry object.
func parseOutputEntry(entry string) outputEntry {
	// Split the entry by spaces, taking care to not split the function call and its arguments.
	fields := strings.Fields(entry)

	if len(fields) > 2 {
		// Parse the timestamp.
		timestamp, err := time.Parse("2006-01-02 15:04:05.000", fields[0]+" "+fields[1])
		if err != nil {
			return outputEntry{
				Message:     entry,
				IsRawString: true,
			}
		}

		level := fields[2]

		// Reconstruct the string from the fourth field onwards to deal with function calls and arguments.
		remainder := strings.Join(fields[4:], " ")

		return outputEntry{
			Timestamp: timestamp,
			Level:     level,
			Message:   remainder,
		}
	}

	return outputEntry{
		Message:     entry,
		IsRawString: true,
	}
}
