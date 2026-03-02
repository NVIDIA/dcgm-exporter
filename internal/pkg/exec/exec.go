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

package exec

import "os/exec"

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/exec/mock_exec.go -package=exec -copyright_file=../../../hack/header.txt . Exec
type Exec interface {
	Command(name string, arg ...string) Cmd
}

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/exec/mock_cmd.go -package=exec -copyright_file=../../../hack/header.txt . Cmd
type Cmd interface {
	Output() ([]byte, error)
}

var (
	_ Exec = (*RealExec)(nil)
	_ Cmd  = (*RealCmd)(nil)
)

type RealExec struct{}

func (r RealExec) Command(name string, arg ...string) Cmd {
	return &RealCmd{cmd: exec.Command(name, arg...)}
}

type RealCmd struct {
	cmd *exec.Cmd
}

func (r *RealCmd) Output() ([]byte, error) {
	return r.cmd.Output()
}
