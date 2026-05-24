{{/*
kacho-nlb helm helpers — standard templates for name / fullname / labels /
selectors + chart-specific helpers (image ref, DB DSN, peer-config render).
*/}}

{{/*
Expand the name of the chart.
*/}}
{{- define "kacho-nlb.name" -}}
{{- default .Chart.Name .Values.name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified name — defaults to .Values.name (chart already namespaced).
*/}}
{{- define "kacho-nlb.fullname" -}}
{{- default (include "kacho-nlb.name" .) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Chart label string ("kacho-nlb-0.2.0").
*/}}
{{- define "kacho-nlb.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Standard labels — merged onto every generated object.
*/}}
{{- define "kacho-nlb.labels" -}}
helm.sh/chart: {{ include "kacho-nlb.chart" . }}
app.kubernetes.io/name: {{ include "kacho-nlb.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: kacho
{{- with .Values.extraLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
Selector labels (matchLabels / Service.selector — must NOT include version /
chart labels, which can change between releases).
*/}}
{{- define "kacho-nlb.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kacho-nlb.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: {{ include "kacho-nlb.name" . }}
{{- end -}}

{{/*
Container image reference — uses .Values.image.tag, falls back to
.Chart.AppVersion if tag is empty (CI bumps appVersion via --set on build).
*/}}
{{- define "kacho-nlb.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Migrator image — defaults to main app image so a single image ships both
binaries (kacho-loadbalancer + kacho-migrator).
*/}}
{{- define "kacho-nlb.migratorImage" -}}
{{- if and .Values.migrator.image.repository .Values.migrator.image.tag -}}
{{- printf "%s:%s" .Values.migrator.image.repository .Values.migrator.image.tag -}}
{{- else -}}
{{- include "kacho-nlb.image" . -}}
{{- end -}}
{{- end -}}

{{/*
ServiceAccount name — honours create=false (uses pre-existing SA).
*/}}
{{- define "kacho-nlb.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kacho-nlb.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
DB Secret name — either pre-existing (.Values.db.existingSecret) or
chart-generated (<fullname>-db).
*/}}
{{- define "kacho-nlb.dbSecretName" -}}
{{- if .Values.db.existingSecret -}}
{{- .Values.db.existingSecret -}}
{{- else -}}
{{- printf "%s-db" (include "kacho-nlb.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
DB Secret key — for existingSecret use .Values.db.existingSecretKey; for the
chart-generated one we always store the password under "password".
*/}}
{{- define "kacho-nlb.dbSecretKey" -}}
{{- if .Values.db.existingSecret -}}
{{- .Values.db.existingSecretKey -}}
{{- else -}}
password
{{- end -}}
{{- end -}}

{{/*
DB DSN template — embeds the password placeholder $(KACHO_NLB_DB_PASSWORD).
Used inside config.yaml (Postgres URL); kacho-nlb resolves the env-var via
viper when the container starts.
*/}}
{{- define "kacho-nlb.dbDSN" -}}
postgres://{{ .Values.db.user }}:$(KACHO_NLB_DB_PASSWORD)@{{ .Values.db.host }}:{{ .Values.db.port }}/{{ .Values.db.name }}?sslmode={{ .Values.db.sslmode }}&search_path={{ .Values.db.name }},public
{{- end -}}

{{/*
ConfigMap name.
*/}}
{{- define "kacho-nlb.configMapName" -}}
{{- printf "%s-config" (include "kacho-nlb.fullname" .) -}}
{{- end -}}
