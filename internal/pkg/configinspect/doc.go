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

// Package configinspect builds the reports of the config show, config explain
// and config validate commands. Each command is a function over the resolved
// configuration that startup uses, so a report says exactly what the exporter
// would run with and where each setting came from. The functions return the
// bytes for stdout and stderr and never write to a terminal; pkg/cmd parses the
// command line, builds the resolved configuration and writes the result.
package configinspect
