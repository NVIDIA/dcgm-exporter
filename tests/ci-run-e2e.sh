#! /bin/bash -x
# Copyright (c) 2019, NVIDIA CORPORATION.  All rights reserved.
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

set -euxo pipefail
shopt -s lastpipe

readonly basedir="$(dirname "$(realpath "$0")")"

# shellcheck source=tests/common.sh
source "${basedir}/common.sh"

# shellcheck source=tests/metrics.sh
source "${basedir}/metrics.sh"

CI_REGISTRY_IMAGE=${CI_REGISTRY_IMAGE:-"undefined"}
CI_COMMIT_SHORT_SHA=${CI_COMMIT_SHORT_SHA:-"undefined"}

install::jq() {
	apt update && apt install -y --no-install-recommends jq
}

install::helm() {
	curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash
}

install::dcgm::exporter() {
	helm package deployment/dcgm-exporter
	helm install --wait dcgm-exporter ./*.tgz --set "image.repository=${CI_REGISTRY_IMAGE}/dcgm-exporter" --set "image.tag=${CI_COMMIT_SHORT_SHA}" --set "serviceMonitor.enabled=true"
}

install::prom() {
	helm repo add stable https://kubernetes-charts.storage.googleapis.com
	helm install --wait stable/prometheus-operator --generate-name \
		--set "prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false"
}

query::prom() {
	IP="$(kubectl get svc -l app=prometheus-operator-prometheus -o jsonpath='{.items[0].spec.clusterIP}')"
	val="$(curl -sL "http://$IP:9090/api/v1/query?query=$1" | jq -r '.data.result')"

	[ "${val}" != "" ] || return 1
	[ "${val}" != "[]" ] || return 1

	echo "$val"
}

query::pod::phase() {
	state="$(kubectl get pods "$1" -o jsonpath='{.status.phase}')"
	[ "$state" = "$2" ] || return 1
}

testing::log::kube() {
	kubectl get pods
	kubectl get svc
	kubectl get serviceMonitor

	kubectl get pods -l "app.kubernetes.io/component=dcgm-exporter" -o yaml
}

install::jq
install::helm
install::prom
install::dcgm::exporter

trap 'testing::log::kube' ERR

for test_case in "metrics"; do
	log INFO "=================Testing ${test_case}================="
	testing::${test_case}::setup "$@"
	testing::${test_case}::main "$@"
	testing::${test_case}::cleanup "$@"
done

