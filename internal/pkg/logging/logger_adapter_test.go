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
	"github.com/go-kit/log/level"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLogrusAdapter_Log(t *testing.T) {
	type testCase struct {
		name    string
		keyvals []interface{}
		assert  func(*testing.T, *logrus.Entry)
	}

	//"msg", "Listening on", "address"
	testCases := []testCase{
		{
			name: "Success",
			keyvals: []interface{}{
				"level",
				level.InfoValue,
				"msg",
				"Listening on",
				"address",
				"127.0.0.0.1:8080",
			},
			assert: func(t *testing.T, entry *logrus.Entry) {
				t.Helper()
				require.NotNil(t, entry)
				assert.Equal(t, "Listening on", entry.Message)
				require.Contains(t, entry.Data, "address")
				assert.Equal(t, "127.0.0.0.1:8080", entry.Data["address"])
			},
		},
		{
			name: "When no Level",
			keyvals: []interface{}{
				"msg",
				"Listening on",
				"address",
				"127.0.0.0.1:8080",
			},
			assert: func(t *testing.T, entry *logrus.Entry) {
				t.Helper()
				require.NotNil(t, entry)
				assert.Equal(t, "Listening on", entry.Message)
				require.Contains(t, entry.Data, "address")
				assert.Equal(t, "127.0.0.0.1:8080", entry.Data["address"])
			},
		},
		{
			name: "When key is not string",
			keyvals: []interface{}{
				"msg",
				"Listening on",
				42,
				"127.0.0.0.1:8080",
			},
			assert: func(t *testing.T, entry *logrus.Entry) {
				t.Helper()
				require.NotNil(t, entry)
				assert.Equal(t, "Listening on", entry.Message)
				require.Contains(t, entry.Data, "missing_key")
				assert.Equal(t, "127.0.0.0.1:8080", entry.Data["missing_key"])
			},
		},
		{
			name: "When value is missing",
			keyvals: []interface{}{
				"msg",
				"Listening on",
				"address",
			},
			assert: func(t *testing.T, entry *logrus.Entry) {
				t.Helper()
				require.NotNil(t, entry)
				assert.Equal(t, "Listening on", entry.Message)
				require.Contains(t, entry.Data, "address")
				assert.Equal(t, "MISSING", entry.Data["address"])
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logrusLogger, logHook := test.NewNullLogger()
			logger := NewLogrusAdapter(logrusLogger)
			err := logger.Log(tc.keyvals...)
			require.NoError(t, err)
			tc.assert(t, logHook.LastEntry())
		})
	}
}
