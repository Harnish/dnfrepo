{{- define "dnfrepo.labels" -}}
app.kubernetes.io/name: dnfrepo
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "dnfrepo.secretEnv" -}}
{{- $s := index .root.Values.secrets .key -}}
{{- if $s.name }}
- name: {{ .env }}
  valueFrom:
    secretKeyRef:
      name: {{ $s.name }}
      key: {{ $s.key }}
{{- end }}
{{- end }}
