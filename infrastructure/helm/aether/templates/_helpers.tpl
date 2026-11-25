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
      {{- if .optional }}
      optional: true
      {{- end }}
{{- end -}}

{{- define "aether.dovecotEnv" -}}
- { name: AETHER_MAIL_HOSTNAME, value: {{ .Values.mailHostname | quote }} }
- { name: AETHER_DOVECOT_MASTER_ALLOW_NETS, value: {{ .Values.dovecotMasterAllowNets | quote }} }
{{- if .Values.tika.enabled }}
- { name: AETHER_TIKA_URL, value: "http://tika:9998/tika/" }
{{- end }}
{{ include "aether.secretEnv" (dict "env" "AETHER_MAIL_DB_PASSWORD" "key" "mailDbPassword" "root" .) }}
{{- with .Values.postgresql.externalHost }}
- { name: AETHER_DB_HOST, value: {{ . | quote }} }
{{- end }}
{{ include "aether.secretEnv" (dict "env" "AETHER_DOVECOT_MASTER_PASSWORD" "key" "dovecotMasterPassword" "root" .) }}
{{ include "aether.secretEnv" (dict "env" "AETHER_DOVECOT_API_KEY" "key" "dovecotApiKey" "root" .) }}
{{ include "aether.secretEnv" (dict "env" "AETHER_SIEVE_RELAY_PASSWORD" "key" "sieveRelayPassword" "root" .) }}
{{ include "aether.secretEnv" (dict "env" "AETHER_SECRET_KEY" "key" "secretKey" "root" .) }}
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

{{- define "aether.spread" -}}
{{- if .root.Values.highAvailability.spreadAcrossNodes -}}
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels: {{- include "aether.selector" . | nindent 14 }}
{{- end }}
{{- end -}}

{{- define "aether.allowFrom" -}}
{{- range . }}
- podSelector:
    matchLabels:
      app.kubernetes.io/name: {{ . }}
{{- end }}
{{- end }}
