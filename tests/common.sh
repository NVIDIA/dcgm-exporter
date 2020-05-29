#! /bin/bash
# Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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

# shellcheck disable=SC2015
[ -t 2 ] && readonly LOG_TTY=1 || readonly LOG_NO_TTY=1

if [ "${LOG_TTY-0}" -eq 1 ] && [ "$(tput colors)" -ge 15 ]; then
	readonly FMT_BOLD=$(tput bold)
	readonly FMT_RED=$(tput setaf 1)
	readonly FMT_YELLOW=$(tput setaf 3)
	readonly FMT_BLUE=$(tput setaf 12)
	readonly FMT_CLEAR=$(tput sgr0)
fi

log() {
	local -r level="$1"; shift
	local -r message="$*"

	local fmt_on="${FMT_CLEAR-}"
	local -r fmt_off="${FMT_CLEAR-}"

	case "${level}" in
		INFO)  fmt_on="${FMT_BLUE-}" ;;
		WARN)  fmt_on="${FMT_YELLOW-}" ;;
		ERROR) fmt_on="${FMT_RED-}" ;;
	esac
	printf "%s[%s]%s %b\n" "${fmt_on}" "${level}" "${fmt_off}" "${message}" >&2
}

with_retry() {
	local -r max_attempts="$1" delay="$2"
	shift 2
	local count=0 rc

	while true; do
		set +e
		"$@"
		rc="$?"
		set -e
		count="$((count+1))"

		if [[ "${rc}" -eq 0 ]]; then
			echo "'$*' SUCCEEDED in ${count} attempts !"
			return 0
		fi

		if [[ "${max_attempts}" -le 0 ]] || [[ "${count}" -lt "${max_attempts}" ]]; then
			echo "'$*' FAILED at attempt ${count}, will retry in ${delay} seconds ..."
			sleep "${delay}"
		else
			break
		fi
	done

	echo "'$*' FAILED in ${count} attempts !"
	return 1
}
