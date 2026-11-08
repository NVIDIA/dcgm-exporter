{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "dcgm-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "dcgm-exporter.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}


{{/*
Allow the release namespace to be overridden for multi-namespace deployments in combined charts
*/}}
{{- define "dcgm-exporter.namespace" -}}
  {{- if .Values.namespaceOverride -}}
    {{- .Values.namespaceOverride -}}
  {{- else -}}
    {{- .Release.Namespace -}}
  {{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "dcgm-exporter.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "dcgm-exporter.labels" -}}
helm.sh/chart: {{ include "dcgm-exporter.chart" . }}
{{ include "dcgm-exporter.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "dcgm-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dcgm-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: {{ include "dcgm-exporter.name" . }}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "dcgm-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
    {{ default (include "dcgm-exporter.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}


{{/*
Determine whether the service account token should be mounted.
*/}}
{{- define "dcgm-exporter.automountServiceAccountToken" -}}
{{- $access := ne (include "dcgm-exporter.kubernetesAccess" .) "" -}}
{{- ternary (and $access .Values.kubernetes.rbac.create) .Values.serviceAccount.automountServiceAccountToken (kindIs "invalid" .Values.serviceAccount.automountServiceAccountToken) -}}
{{- end -}}


{{/*
Create the name of the tls secret to use
*/}}
{{- define "dcgm-exporter.tlsCertsSecretName" -}}
{{- if .Values.tlsServerConfig.existingSecret -}}
    {{- printf "%s" (tpl .Values.tlsServerConfig.existingSecret $) -}}
{{- else -}}
    {{ printf "%s-tls" (include "dcgm-exporter.fullname" .) }}
{{- end -}}
{{- end -}}


{{/*
Return "true" when the chart renders an exporter web config: TLS or basic auth is on.
*/}}
{{- define "dcgm-exporter.webConfigEnabled" -}}
{{- if or .Values.tlsServerConfig.enabled .Values.basicAuth.users -}}
true
{{- end -}}
{{- end -}}

{{/*
Return the path of the exporter config file the chart mounts, which
DCGM_EXPORTER_CONFIG_FILE names. Each config file the chart mounts (exporter
config, metrics list, web config) sits alone in a directory under
/etc/dcgm-exporter/chart, which the image does not use. The chart
mounts that directory, never the file with subPath, so the kubelet keeps the
file current; daemonset.yaml takes the directory with `dir`.
*/}}
{{- define "dcgm-exporter.configFilePath" -}}
{{- printf "/etc/dcgm-exporter/chart/config/%s" (include "dcgm-exporter.configMap" . | fromJson).key -}}
{{- end -}}

{{/*
Return the path of the metrics list (customMetrics, or the default list) the
chart mounts. The exporter reads it only when its metrics file setting names
this path.
*/}}
{{- define "dcgm-exporter.metricsFilePath" -}}
/etc/dcgm-exporter/chart/metrics/default-counters.csv
{{- end -}}

{{/*
Return the name of the ConfigMap that holds the metrics list: the fullname
and a "-metrics" suffix. A fullname too long for the suffix to fit in 63
characters is cut and followed by a hash of the whole fullname, so two releases
whose fullnames differ only after the cut still get different names.
*/}}
{{- define "dcgm-exporter.metricsConfigMapName" -}}
{{- $fullname := include "dcgm-exporter.fullname" . -}}
{{- if le (len $fullname) 55 -}}
{{- printf "%s-metrics" $fullname -}}
{{- else -}}
{{- printf "%s-%s-metrics" ($fullname | trunc 46 | trimSuffix "-") ($fullname | sha256sum | trunc 8) -}}
{{- end -}}
{{- end -}}

{{/*
Return the path of the web config the chart renders, inside the mounted Secret.
*/}}
{{- define "dcgm-exporter.webConfigPath" -}}
/etc/dcgm-exporter/chart/web-config/web-config.yaml
{{- end -}}

{{/*
Create the name of the web-config Secret to use.
*/}}
{{- define "dcgm-exporter.webConfigSecretName" -}}
{{- printf "%s-web-config" (include "dcgm-exporter.fullname" .) -}}
{{- end -}}

{{/*
Return, as JSON, the configMap values with their defaults filled in. Values that
--reuse-values keeps from an older chart may have no configMap, and a blank key
would render an invalid mount. Read it with
`include "dcgm-exporter.configMap" . | fromJson`.
*/}}
{{- define "dcgm-exporter.configMap" -}}
{{- $configMap := .Values.configMap | default dict -}}
{{- dict "existingName" ($configMap.existingName | default "") "key" ($configMap.key | default "config.yaml") | toJson -}}
{{- end -}}

{{/*
Return, as JSON, the exporter config the chart renders: `config`, or {} when
configMap.existingName is set or `config` is null or not a map. Read it with
`include "dcgm-exporter.config" . | fromJson`.
*/}}
{{- define "dcgm-exporter.config" -}}
{{- if and (kindIs "map" .Values.config) (not (include "dcgm-exporter.configMap" . | fromJson).existingName) -}}
{{- toJson .Values.config -}}
{{- else -}}
{}
{{- end -}}
{{- end -}}

{{/*
Return the exporter config file the chart writes into its ConfigMap, or nothing
when it creates no ConfigMap. With TLS or basic auth on, the file names the web
config the chart mounts.
*/}}
{{- define "dcgm-exporter.configFile" -}}
{{- $config := include "dcgm-exporter.config" . | fromJson -}}
{{- if $config -}}
  {{- /* The exporter needs an integer version; --set-string or a quoted "2" would render a string. */ -}}
  {{- $_ := set $config "version" (int $config.version) -}}
  {{- if include "dcgm-exporter.webConfigEnabled" . -}}
    {{- $server := $config.server | default dict -}}
    {{- $_ := set $server "web_config_file" (include "dcgm-exporter.webConfigPath" .) -}}
    {{- $_ = set $config "server" $server -}}
  {{- end -}}
  {{- toYaml $config -}}
{{- end -}}
{{- end -}}

{{/*
Fail when values cannot render a working exporter. daemonset.yaml includes it
once; other templates read values without checking them again.
*/}}
{{- /*
Kubernetes accepts a DaemonSet rolling update only when exactly one of
maxUnavailable and maxSurge is nonzero. 0 and "0%" both count as zero. A null
value gets the Kubernetes default: 1 for maxUnavailable, 0 for maxSurge.
*/ -}}
{{- define "dcgm-exporter.validateRollingUpdate" -}}
{{- with .Values.rollingUpdate -}}
  {{- $unavailable := or (kindIs "invalid" .maxUnavailable) (ne (toString .maxUnavailable | trimSuffix "%") "0") -}}
  {{- $surge := and (not (kindIs "invalid" .maxSurge)) (ne (toString .maxSurge | trimSuffix "%") "0") -}}
  {{- if and $unavailable $surge -}}
    {{- fail (printf "rollingUpdate.maxUnavailable (%v) and rollingUpdate.maxSurge (%v) cannot both be nonzero; set one of them to 0" .maxUnavailable .maxSurge) -}}
  {{- end -}}
  {{- if not (or $unavailable $surge) -}}
    {{- fail "rollingUpdate.maxUnavailable and rollingUpdate.maxSurge cannot both be 0; set one of them above 0" -}}
  {{- end -}}
{{- end -}}
{{- end -}}

{{- define "dcgm-exporter.validateValues" -}}
{{- if and (not (kindIs "invalid" .Values.config)) (not (kindIs "map" .Values.config)) -}}
  {{- fail "config must be a YAML map (the exporter configuration) or null" -}}
{{- end -}}
{{- $outputs := (.Values.config | default dict).outputs -}}
{{- if and (not (kindIs "invalid" $outputs)) (not (kindIs "map" $outputs)) -}}
  {{- fail "config.outputs must be a map of output names to their settings" -}}
{{- end -}}

{{- include "dcgm-exporter.rejectRemovedValues" . -}}
{{- include "dcgm-exporter.rejectSettingOverrides" . -}}
{{- include "dcgm-exporter.rejectImageMetricsMount" . -}}
{{- include "dcgm-exporter.rejectUnreadCustomMetrics" . -}}
{{- include "dcgm-exporter.validateRollingUpdate" . -}}
{{- $config := include "dcgm-exporter.config" . | fromJson -}}
{{- if and $config (ne (printf "%v" $config.version) "2") -}}
  {{- fail "config.version must be 2; to mount a version 1 file, set configMap.existingName" -}}
{{- end -}}

{{- /*
The container port, probes, Service and NetworkPolicy use service.port, so every
output must listen on it. An output without `listen` uses the exporter default, :9400.
*/ -}}
{{- $port := toString .Values.service.port -}}
{{- range $name, $output := $config.outputs -}}
  {{- if kindIs "map" $output -}}
    {{- $listen := toString ($output.listen | default ":9400") -}}
    {{- if not (regexMatch "^(\\[[^]]+\\]|[^:]*):[0-9]+$" $listen) -}}
      {{- fail (printf "config.outputs.%s.listen (%q) must be HOST:PORT with a numeric TCP port (IPv6 hosts in brackets)" $name $listen) -}}
    {{- end -}}
    {{- if ne (last (splitList ":" $listen)) $port -}}
      {{- fail (printf "config.outputs.%s.listen (%q) must use service.port (%s), which the container port, probes, Service and NetworkPolicy use; change both together" $name $listen $port) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{- /* The web config the chart renders for TLS or basic auth reaches the exporter through its config file. */ -}}
{{- if and (include "dcgm-exporter.webConfigEnabled" .) (not (include "dcgm-exporter.configMap" . | fromJson).existingName) -}}
  {{- if not $config -}}
    {{- fail "tlsServerConfig and basicAuth need the chart's config file to set server.web_config_file; set `config` to a version 2 map instead of null" -}}
  {{- end -}}
  {{- $file := toString (($config.server | default dict).web_config_file | default "") -}}
  {{- $path := include "dcgm-exporter.webConfigPath" . -}}
  {{- if and $file (ne (include "dcgm-exporter.configRelativePath" (list . $file)) $path) -}}
    {{- fail (printf "config.server.web_config_file (%q) differs from the web config the chart renders for tlsServerConfig or basicAuth (%q); remove config.server.web_config_file" $file $path) -}}
  {{- end -}}
{{- end -}}
{{- end -}}

{{/*
Fail when an extraVolumeMounts entry mounts at the image's metrics list path,
the chart renders `config`, and no config.metrics[].file names that path. Older
values files mounted customMetrics there; the chart mounts it at
metricsFilePath instead.
With configMap.existingName or `config: null` the chart cannot see which file
the exporter reads, so it does not check.
*/}}
{{- define "dcgm-exporter.rejectImageMetricsMount" -}}
{{- $imageMetricsFile := "/etc/dcgm-exporter/default-counters.csv" -}}
{{- $config := include "dcgm-exporter.config" . | fromJson -}}
{{- if and $config (not (has $imageMetricsFile (include "dcgm-exporter.configMetricsFiles" . | fromJsonArray))) -}}
  {{- range .Values.extraVolumeMounts -}}
    {{- if and (kindIs "map" .) (eq (clean (toString .mountPath)) $imageMetricsFile) -}}
      {{- fail (printf "extraVolumeMounts entry %q mounts %s, but no config.metrics[].file names that path: the chart mounts the metrics list itself, at %s. Remove that entry and its extraConfigMapVolumes volume. To change the metrics list, list the metrics in config.metrics[].fields, or point config.metrics[].file at your own file" (toString .name) $imageMetricsFile (include "dcgm-exporter.metricsFilePath" $)) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- end -}}

{{/*
Fail when customMetrics is set but the chart's config reads another metrics
list. With configMap.existingName or `config: null` the chart cannot see which
file the exporter reads; NOTES.txt names the path instead.
*/}}
{{- define "dcgm-exporter.rejectUnreadCustomMetrics" -}}
{{- $config := include "dcgm-exporter.config" . | fromJson -}}
{{- $metricsFile := include "dcgm-exporter.metricsFilePath" . -}}
{{- if and .Values.customMetrics $config (not (has $metricsFile (include "dcgm-exporter.configMetricsFiles" . | fromJsonArray))) -}}
  {{- fail (printf "customMetrics is set, but no config.metrics[].file names %s, where the chart mounts it, so the exporter would not read it; set config.metrics[0].file to that path, or remove customMetrics. customMetrics is deprecated; use config.metrics[].fields instead" $metricsFile) -}}
{{- end -}}
{{- end -}}

{{/*
Return, as a JSON list, the metrics[].file values of the chart's config, each
resolved as the exporter reads it (see dcgm-exporter.configRelativePath).
*/}}
{{- define "dcgm-exporter.configMetricsFiles" -}}
{{- $files := list -}}
{{- $config := include "dcgm-exporter.config" . | fromJson -}}
{{- if and $config (kindIs "slice" $config.metrics) -}}
  {{- range $config.metrics -}}
    {{- if and (kindIs "map" .) .file -}}
      {{- $files = append $files (include "dcgm-exporter.configRelativePath" (list $ (toString .file))) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- toJson $files -}}
{{- end -}}

{{/*
Return a path from the chart's config file as the exporter resolves it: a
relative path is trimmed and starts from the config file's directory, and the
result is cleaned. Takes a list of the root context and the path.
*/}}
{{- define "dcgm-exporter.configRelativePath" -}}
{{- $path := index . 1 -}}
{{- $trimmed := trim $path -}}
{{- if and $trimmed (not (isAbs $trimmed)) -}}
  {{- $path = printf "%s/%s" (include "dcgm-exporter.configFilePath" (index . 0) | dir) $trimmed -}}
{{- end -}}
{{- clean $path -}}
{{- end -}}

{{/*
Fail when values still set removed chart values, and say where each moved.
*/}}
{{- define "dcgm-exporter.rejectRemovedValues" -}}
{{- $found := list -}}
{{- $moves := list -}}
{{- range $name := list "enabled" "create" "name" "key" "mountPath" "data" -}}
  {{- if hasKey ($.Values.config | default dict) $name -}}
    {{- $found = append $found (printf "config.%s" $name) -}}
  {{- end -}}
{{- end -}}
{{- $configPath := printf "%s/<configMap.key>" (include "dcgm-exporter.configFilePath" . | dir) -}}
{{- if $found -}}
  {{- $moves = list
        "config.data -> `config`, rewritten as a YAML map with `version: 2`; the 4.8.4 version 2 keys changed (for example `collections`), so check the result with `dcgm-exporter config validate`"
        "config.key -> configMap.key"
        (printf "config.mountPath -> remove it; the chart mounts the config file at %s" $configPath)
        "config.create -> remove it; the chart creates a ConfigMap from `config` unless configMap.existingName is set"
        "config.name -> configMap.existingName, for a ConfigMap you manage"
        "config.enabled: false -> `config: null`" -}}
{{- end -}}
{{- /* Each entry: the values block, the removed value in it, and its replacement. */ -}}
{{- $removed := list
      (list "configMap" "create" "remove it; the chart creates a ConfigMap from `config` unless configMap.existingName is set")
      (list "configMap" "mountPath" (printf "remove it; the chart mounts the config file at %s and sets DCGM_EXPORTER_CONFIG_FILE to that path" $configPath))
      (list "service" "address" "config.outputs.<name>.listen, with the port in service.port")
      (list "service" "webReadTimeout" "config.server.read_timeout")
      (list "service" "webWriteTimeout" "config.server.write_timeout")
      (list "kubernetes" "enablePodLabels" "config.sources.kubernetes.pod_labels.enabled")
      (list "kubernetes" "podLabelAllowlistRegex" "config.sources.kubernetes.pod_labels.allow")
      (list "kubernetes" "enablePodUID" "config.sources.kubernetes.pod_uid")
      (list "kubernetesDRA" "enabled" "config.metrics[0].enrichment.dra: true, with config.sources.dra.mode: kubernetes")
      (list "debugDump" "enabled" "config.debug_dump.enabled")
      (list "debugDump" "directory" "config.debug_dump.directory")
      (list "debugDump" "retention" "config.debug_dump.retention, a duration such as 24h")
      (list "debugDump" "compression" "config.debug_dump.compression") -}}
{{- range $removed -}}
  {{- $block := index $.Values (index . 0) -}}
  {{- if and (kindIs "map" $block) (hasKey $block (index . 1)) -}}
    {{- $name := printf "%s.%s" (index . 0) (index . 1) -}}
    {{- $found = append $found $name -}}
    {{- $moves = append $moves (printf "%s -> %s" $name (index . 2)) -}}
  {{- end -}}
{{- end -}}
{{- if and $found (include "dcgm-exporter.configMap" . | fromJson).existingName -}}
  {{- $moves = append $moves "with configMap.existingName, `config` is not used: set these keys without the `config.` prefix in that file, or for a version 1 file pass the matching flags in `arguments`" -}}
{{- end -}}
{{- if $found -}}
  {{- fail (printf "%s: removed chart values. In your values file, set the new keys instead:\n  %s\nThen upgrade with that file: `helm upgrade <release> <chart> -f <values file>`. `helm get values <release> -o yaml` prints the values you set, as a starting file. Do not add `--reuse-values` or `--reset-then-reuse-values`: they bring back the removed values stored with the release." (join ", " $found) (join "\n  " $moves)) -}}
{{- end -}}
{{- end -}}

{{/*
Fail when arguments or extraEnv override a setting that objects or checks of
the chart depend on, while the chart renders `config`: the listen address (the
container port, probes and Service use service.port), the metrics file (the
customMetrics and image-mount checks read config.metrics[].file), the web
config file the chart mounts, the debug dump settings that decide its emptyDir,
and the Kubernetes enrichment settings that decide its RBAC and token mount. Other
settings may be overridden, because the exporter applies flags and environment
variables over its config file.
*/}}
{{- define "dcgm-exporter.rejectSettingOverrides" -}}
{{- if include "dcgm-exporter.config" . | fromJson -}}
{{- /* Each entry: the config key, its flag names, and its environment variable. */ -}}
{{- $settings := list
      (list "outputs.<name>.listen" (list "address" "a") "DCGM_EXPORTER_LISTEN")
      (list "metrics[].file" (list "collectors" "f") "DCGM_EXPORTER_COLLECTORS")
      (list "server.web_config_file" (list "web-config-file") "DCGM_EXPORTER_WEB_CONFIG_FILE")
      (list "debug_dump.enabled" (list "dump-enabled") "DCGM_EXPORTER_DUMP_ENABLED")
      (list "debug_dump.directory" (list "dump-directory") "DCGM_EXPORTER_DUMP_DIRECTORY")
      (list "sources.kubernetes.pod_labels.enabled" (list "kubernetes-enable-pod-labels") "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS")
      (list "sources.kubernetes.pod_uid" (list "kubernetes-enable-pod-uid") "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_UID")
      (list "metrics[0].enrichment.dra" (list "kubernetes-enable-dra") "KUBERNETES_ENABLE_DRA") -}}
{{- range $settings -}}
  {{- $key := index . 0 -}}
  {{- $flags := index . 1 -}}
  {{- $env := index . 2 -}}
  {{- $error := printf "set config.%s, not --%s or %s: objects or checks of the chart depend on this setting" $key (first $flags) $env -}}
  {{- range $.Values.arguments -}}
    {{- $arg := toString . -}}
    {{- if hasPrefix "-" $arg -}}
      {{- $name := trimPrefix "-" (trimPrefix "-" $arg) -}}
      {{- range $flag := $flags -}}
        {{- if or (eq $name $flag) (hasPrefix (printf "%s=" $flag) $name) -}}
          {{- fail $error -}}
        {{- end -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
  {{- range $.Values.extraEnv -}}
    {{- if eq .name $env -}}
      {{- fail $error -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Return the widest Kubernetes API read the chart's config needs, as a plain string:
  (empty)         none
  pods            pod labels or pod UID are on
  resourceSlices  DRA labels are on (metrics[0].enrichment.dra not false, with
                  a sources.dra block); this also needs pods
With configMap.existingName the chart cannot read the file, so it needs nothing.
*/}}
{{- define "dcgm-exporter.kubernetesAccess" -}}
{{- $config := include "dcgm-exporter.config" . | fromJson -}}
{{- $sources := $config.sources | default dict -}}
{{- $kubernetes := $sources.kubernetes | default dict -}}
{{- $dra := false -}}
{{- if and (kindIs "slice" $config.metrics) $config.metrics $sources.dra -}}
  {{- $entry := index $config.metrics 0 -}}
  {{- if kindIs "map" $entry -}}
    {{- /* Like the exporter, a missing switch means auto, so only false turns DRA off. */ -}}
    {{- $dra = not (has (toString (($entry.enrichment | default dict).dra)) (list "false" "False" "FALSE")) -}}
  {{- end -}}
{{- end -}}
{{- if $dra -}}
resourceSlices
{{- else if or ($kubernetes.pod_labels | default dict).enabled $kubernetes.pod_uid -}}
pods
{{- end -}}
{{- end -}}

{{/*
Render initialDelaySeconds for the liveness or readiness probe. A value the
user sets always renders. Otherwise the probe needs no delay while the startup
probe is on, because Kubernetes starts it only after the startup probe
succeeds; without a startup probe it waits 45 seconds so a slow start is not
restarted. Call with (dict "probe" .Values.livenessProbe "root" .).
*/}}
{{- define "dcgm-exporter.probeInitialDelay" -}}
{{- $root := .root -}}
{{- if and .probe (hasKey .probe "initialDelaySeconds") (not (kindIs "invalid" .probe.initialDelaySeconds)) }}
initialDelaySeconds: {{ .probe.initialDelaySeconds }}
{{- else if not (and $root.Values.startupProbe $root.Values.startupProbe.enabled) }}
initialDelaySeconds: 45
{{- end }}
{{- end -}}
