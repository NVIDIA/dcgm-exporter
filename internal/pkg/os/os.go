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

package os

import "os"

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/os/mock_os.go -package=os -copyright_file=../../../hack/header.txt . OS
//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/os/mock_dir_entry.go -package=os -copyright_file=../../../hack/header.txt os DirEntry
//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/os/mock_file_info.go -package=os -copyright_file=../../../hack/header.txt io/fs FileInfo
type OS interface {
	CreateTemp(dir, pattern string) (*os.File, error)
	Getenv(key string) string
	Hostname() (string, error)
	IsNotExist(err error) bool
	MkdirTemp(dir, pattern string) (string, error)
	Open(name string) (*os.File, error)
	Remove(name string) error
	RemoveAll(path string) error
	Stat(name string) (os.FileInfo, error)
	TempDir() string
	ReadDir(name string) ([]os.DirEntry, error)
	Exit(code int)
}

type RealOS struct{}

func (RealOS) Hostname() (string, error) {
	return os.Hostname()
}

func (RealOS) Getenv(key string) string {
	return os.Getenv(key)
}

func (RealOS) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}

func (RealOS) IsNotExist(err error) bool {
	return os.IsNotExist(err)
}

func (RealOS) Open(name string) (*os.File, error) {
	return os.Open(name)
}

func (RealOS) MkdirTemp(dir, pattern string) (string, error) {
	return os.MkdirTemp(dir, pattern)
}

func (RealOS) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

func (RealOS) CreateTemp(dir, pattern string) (*os.File, error) {
	return os.CreateTemp(dir, pattern)
}

func (RealOS) TempDir() string {
	return os.TempDir()
}

func (RealOS) Remove(name string) error {
	return os.Remove(name)
}

func (RealOS) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(name)
}

func (RealOS) Exit(code int) { os.Exit(code) }
