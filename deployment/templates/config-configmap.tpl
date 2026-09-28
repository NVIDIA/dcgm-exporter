{{- if and .Values.config.enabled .Values.config.create }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "dcgm-exporter.configConfigMap" . }}
  namespace: {{ include "dcgm-exporter.namespace" . }}
  labels:
    {{- include "dcgm-exporter.labels" . | nindent 4 }}
data:
  {{ .Values.config.key | quote }}: |
{{- .Values.config.data | nindent 4 }}
{{- end }}
