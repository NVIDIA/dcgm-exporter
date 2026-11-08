//go:build cgo

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

package testhelper

/*
#include <stdio.h>

static void print_capture_test_message(void) {
	const char message[] = "C output\n";
	fputs(message, stdout);
	fflush(stdout);
}
*/
import "C"

// WriteCaptureTestMessage writes through C's stdout stream so Capture tests
// can prove it redirects output that bypasses Go's os.Stdout writer.
func WriteCaptureTestMessage() {
	C.print_capture_test_message()
}
