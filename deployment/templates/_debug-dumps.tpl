{{/*
Return the directory where the chart mounts its debug dump emptyDir, or nothing.
The chart mounts one when config.debug_dump.enabled is true, configMap.existingName
is not set, and values supply no volume named debug-dumps.
*/}}
{{- define "dcgm-exporter.dumpVolumeDirectory" -}}
{{- $userVolume := false -}}
{{- range concat (.Values.extraHostVolumes | default list) (.Values.extraConfigMapVolumes | default list) -}}
  {{- if eq .name "debug-dumps" -}}{{- $userVolume = true -}}{{- end -}}
{{- end -}}
{{- $dump := (include "dcgm-exporter.config" . | fromJson).debug_dump | default dict -}}
{{- if and (not $userVolume) $dump.enabled -}}
  {{- $dump.directory | default "/tmp/dcgm-exporter-debug" -}}
{{- end -}}
{{- end -}}
