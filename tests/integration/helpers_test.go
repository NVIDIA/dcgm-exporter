/*
 * Copyright (c) 2023, NVIDIA CORPORATION.  All rights reserved.
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

package integration

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var randomPortMutex sync.Mutex

var usedPorts = map[int]struct{}{}

func getRandomAvailablePort(t *testing.T) int {
	randomPortMutex.Lock()
	defer randomPortMutex.Unlock()
	t.Helper()
retry:
	addr, err := net.ResolveTCPAddr("tcp", ":0")
	require.NoError(t, err)
	l, err := net.ListenTCP("tcp", addr)
	require.NoError(t, err)
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if _, exist := usedPorts[port]; exist {
		goto retry
	}
	usedPorts[port] = struct{}{}
	return port
}

func httpGet(t *testing.T, url string, customClient ...*http.Client) (string, int, error) {
	t.Helper()

	client := http.DefaultClient

	if len(customClient) > 0 {
		client = customClient[0]
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", -1, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", -1, err
	}
	return string(body), resp.StatusCode, nil
}

func newRequestWithBasicAuth(t *testing.T, username, password, method string, url string, body io.Reader) *http.Request {
	t.Helper()
	auth := username + ":" + password
	authorizationValue := base64.StdEncoding.EncodeToString([]byte(auth))
	req, err := http.NewRequest(method, url, body)
	require.NoError(t, err)
	req.Header.Add("Authorization", "Basic "+authorizationValue)
	return req
}
