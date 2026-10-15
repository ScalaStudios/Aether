{{- define "aether.secretName" -}}
{{- default (printf "%s-secrets" .Release.Name) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "aether.image" -}}
{{- printf "%s/aether-%s:%s" .root.Values.image.registry .name .root.Values.image.tag -}}
{{- end -}}

{{- define "aether.labels" -}}
app.kubernetes.io/part-of: aether
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "aether.selector" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end -}}

{{- define "aether.secretEnv" -}}
- name: {{ .env }}
  valueFrom:
    secretKeyRef:
      name: {{ include "aether.secretName" .root }}
      key: {{ .key }}
{{- end -}}

{{- define "aether.podSecurity" -}}
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "aether.mailDaemonSecurity" -}}
allowPrivilegeEscalation: false
capabilities:
  drop: ["ALL"]
  add: ["CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID", "NET_BIND_SERVICE", "KILL", "SYS_CHROOT"]
{{- end -}}

{{- define "aether.restrictedSecurity" -}}
allowPrivilegeEscalation: false
runAsNonRoot: true
capabilities:
  drop: ["ALL"]
{{- end -}}

{{- define "aether.tlsVolume" -}}
- name: mail-tls
  secret:
    secretName: {{ .Values.mailTLS.secretName }}
    items:
      - { key: tls.crt, path: fullchain.pem }
      - { key: tls.key, path: privkey.pem, mode: 0400 }
{{- end -}}

{{- define "aether.pvc" -}}
accessModes: ["ReadWriteOnce"]
{{- with .root.Values.storageClass }}
storageClassName: {{ . }}
{{- end }}
resources:
  requests:
    storage: {{ .size }}
{{- end -}}

{{- define "aether.allowFrom" -}}
{{- range . }}
- podSelector:
    matchLabels:
      app.kubernetes.io/name: {{ . }}
{{- end }}
{{- end }}
