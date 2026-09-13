{{- define "gpu.fullname" -}}
{{- if gt (len .Release.Name) 40 }}{{ fail "Release names must be at most 40 characters" }}{{ end -}}
{{- .Release.Name -}}
{{- end -}}
{{- define "gpu.labels" -}}
app.kubernetes.io/name: gpu-telemetry
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}
{{- define "gpu.selector" -}}
app.kubernetes.io/name: gpu-telemetry
app.kubernetes.io/instance: {{ .root.Release.Name | quote }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}
{{- define "gpu.secret" -}}
{{- if .Values.database.existingSecret -}}{{ .Values.database.existingSecret }}{{- else -}}{{ include "gpu.fullname" . }}-database{{- end -}}
{{- end -}}
{{- define "gpu.image" -}}
{{ printf "%s/%s:%s" .root.Values.images.repository .component .root.Values.images.tag | quote }}
{{- end -}}
{{- define "gpu.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}
{{- define "gpu.podSecurity" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
fsGroupChangePolicy: OnRootMismatch
seccompProfile:
  type: RuntimeDefault
{{- end -}}
{{- define "gpu.databaseEnv" -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "gpu.secret" . }}
      key: database-url
{{- end -}}
