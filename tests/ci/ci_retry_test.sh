#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# This script unit-tests the bounded retry helper used by CI jobs.

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RETRY_SCRIPT="${ROOT_DIR}/hack/ci/retry.sh"
test_count=0

# write_stub creates an executable command stub for shell fixture tests.
write_stub() {
    local path="$1"
    local body="$2"
    printf '%s\n' "${body}" >"${path}"
    chmod +x "${path}"
}

# assert_contains fails when a file does not contain an expected literal string.
assert_contains() {
    local file="$1"
    local pattern="$2"
    if ! grep -Fq -- "${pattern}" "${file}"; then
        echo "Expected ${file} to contain ${pattern}" >&2
        sed -n '1,220p' "${file}" >&2 || true
        return 1
    fi
}

# assert_not_contains fails when a file contains an unexpected literal string.
assert_not_contains() {
    local file="$1"
    local pattern="$2"
    if grep -Fq -- "${pattern}" "${file}"; then
        echo "Expected ${file} not to contain ${pattern}" >&2
        sed -n '1,220p' "${file}" >&2 || true
        return 1
    fi
}

# assert_equals fails when two strings differ.
assert_equals() {
    local want="$1"
    local got="$2"
    if [[ "${got}" != "${want}" ]]; then
        echo "Expected ${want}, got ${got}" >&2
        return 1
    fi
}

# run_retry captures retry.sh output and returns its exit code to the caller.
run_retry() {
    local stdout_file="$1"
    local stderr_file="$2"
    shift 2
    "${RETRY_SCRIPT}" "$@" >"${stdout_file}" 2>"${stderr_file}"
}

# run_test prints a small TAP-like wrapper around each shell test function.
run_test() {
    local name="$1"
    shift
    test_count=$((test_count + 1))
    printf '=== RUN   %s\n' "${name}"
    "$@"
    printf -- '--- PASS: %s\n' "${name}"
}

# test_retry_requires_command verifies retry.sh rejects an empty command.
test_retry_requires_command() {
    local tmp stdout_file stderr_file
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"

    if run_retry "${stdout_file}" "${stderr_file}"; then
        echo "Expected retry.sh to fail without a command" >&2
        return 1
    fi

    assert_contains "${stderr_file}" "usage:"
}

# test_retry_rejects_unknown_flag verifies only documented retry flags are accepted.
test_retry_rejects_unknown_flag() {
    local tmp stdout_file stderr_file status
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"

    set +e
    run_retry "${stdout_file}" "${stderr_file}" --bogus
    status=$?
    set -e
    assert_equals "2" "${status}"
    assert_contains "${stderr_file}" "usage:"
}

# test_retry_rejects_missing_flag_value verifies options that require values fail with usage.
test_retry_rejects_missing_flag_value() {
    local tmp stdout_file stderr_file status
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"

    set +e
    run_retry "${stdout_file}" "${stderr_file}" --attempts
    status=$?
    set -e
    assert_equals "2" "${status}"
    assert_contains "${stderr_file}" "usage:"
}

# test_retry_validates_attempts verifies attempts must be a positive integer.
test_retry_validates_attempts() {
    local tmp stdout_file stderr_file status
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"

    set +e
    run_retry "${stdout_file}" "${stderr_file}" --attempts 0 -- true
    status=$?
    set -e
    assert_equals "2" "${status}"
    assert_contains "${stderr_file}" "attempts must be a positive integer"
}

# test_retry_rejects_nonnumeric_attempts verifies attempts cannot be arbitrary text.
test_retry_rejects_nonnumeric_attempts() {
    local tmp stdout_file stderr_file status
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"

    set +e
    run_retry "${stdout_file}" "${stderr_file}" --attempts many -- true
    status=$?
    set -e
    assert_equals "2" "${status}"
    assert_contains "${stderr_file}" "attempts must be a positive integer"
}

# test_retry_validates_delay verifies delay must be a non-negative integer.
test_retry_validates_delay() {
    local tmp stdout_file stderr_file status
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"

    set +e
    run_retry "${stdout_file}" "${stderr_file}" --delay nope -- true
    status=$?
    set -e
    assert_equals "2" "${status}"
    assert_contains "${stderr_file}" "delay must be a non-negative integer"
}

# test_retry_succeeds_after_transient_failures verifies retry stops once the command succeeds.
test_retry_succeeds_after_transient_failures() {
    local tmp stdout_file stderr_file counter_file bin_dir
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"
    counter_file="${tmp}/counter"
    bin_dir="${tmp}/bin"
    mkdir -p "${bin_dir}"
    printf '0\n' >"${counter_file}"
    write_stub "${bin_dir}/flaky" "#!/usr/bin/env bash
set -euo pipefail
counter=\$(cat '${counter_file}')
counter=\$((counter + 1))
printf '%s\n' \"\${counter}\" >'${counter_file}'
if [[ \"\${counter}\" -lt 3 ]]; then
  exit 17
fi
printf '%s\n' success
"

    run_retry "${stdout_file}" "${stderr_file}" --attempts 5 --delay 0 -- "${bin_dir}/flaky"

    assert_equals "3" "$(cat "${counter_file}")"
    assert_contains "${stdout_file}" "success"
    assert_contains "${stderr_file}" "retrying in 0s (1/5)"
    assert_contains "${stderr_file}" "retrying in 0s (2/5)"
    assert_not_contains "${stderr_file}" "failed after"
}

# test_retry_stops_after_configured_attempts verifies permanent failures are bounded.
test_retry_stops_after_configured_attempts() {
    local tmp stdout_file stderr_file counter_file bin_dir status
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"
    counter_file="${tmp}/counter"
    bin_dir="${tmp}/bin"
    mkdir -p "${bin_dir}"
    printf '0\n' >"${counter_file}"
    write_stub "${bin_dir}/always-fails" "#!/usr/bin/env bash
set -euo pipefail
counter=\$(cat '${counter_file}')
counter=\$((counter + 1))
printf '%s\n' \"\${counter}\" >'${counter_file}'
exit 7
"

    set +e
    run_retry "${stdout_file}" "${stderr_file}" --attempts 3 --delay 0 -- "${bin_dir}/always-fails"
    status=$?
    set -e

    assert_equals "7" "${status}"
    assert_equals "3" "$(cat "${counter_file}")"
    assert_contains "${stderr_file}" "Command failed after 3 attempts:"
}

# test_retry_does_not_retry_after_success verifies successful first attempts run only once.
test_retry_does_not_retry_after_success() {
    local tmp stdout_file stderr_file counter_file bin_dir
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-retry-test.XXXXXX")"
    stdout_file="${tmp}/stdout"
    stderr_file="${tmp}/stderr"
    counter_file="${tmp}/counter"
    bin_dir="${tmp}/bin"
    mkdir -p "${bin_dir}"
    printf '0\n' >"${counter_file}"
    write_stub "${bin_dir}/succeeds" "#!/usr/bin/env bash
set -euo pipefail
counter=\$(cat '${counter_file}')
counter=\$((counter + 1))
printf '%s\n' \"\${counter}\" >'${counter_file}'
printf '%s\n' ok
"

    run_retry "${stdout_file}" "${stderr_file}" --attempts 5 --delay 0 -- "${bin_dir}/succeeds"

    assert_equals "1" "$(cat "${counter_file}")"
    assert_contains "${stdout_file}" "ok"
    assert_not_contains "${stderr_file}" "retrying"
}

run_test test_retry_requires_command test_retry_requires_command
run_test test_retry_rejects_unknown_flag test_retry_rejects_unknown_flag
run_test test_retry_rejects_missing_flag_value test_retry_rejects_missing_flag_value
run_test test_retry_validates_attempts test_retry_validates_attempts
run_test test_retry_rejects_nonnumeric_attempts test_retry_rejects_nonnumeric_attempts
run_test test_retry_validates_delay test_retry_validates_delay
run_test test_retry_succeeds_after_transient_failures test_retry_succeeds_after_transient_failures
run_test test_retry_stops_after_configured_attempts test_retry_stops_after_configured_attempts
run_test test_retry_does_not_retry_after_success test_retry_does_not_retry_after_success

printf 'PASS: %d CI retry shell tests\n' "${test_count}"
