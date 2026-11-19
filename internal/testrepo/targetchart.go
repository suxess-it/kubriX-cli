package testrepo

import "testing"

// targetChartApplication mirrors kubriX's platform-apps/target-chart/templates/oss/application.yaml: applications are
// a list, or with applicationMap a map whose enabled entries are installed; each Application gets the default value
// files, then its own, its helmOptions and the plan and source parameters.
const targetChartApplication = `{{- $applications := .Values.applications }}
{{- if .Values.applicationMap }}
{{- if not (kindIs "map" $applications) }}
{{- fail "applicationMap=true requires applications to be a map" }}
{{- end }}
{{- $applications = list }}
{{- range $name, $application := .Values.applications }}
{{- if $application.enabled }}
{{- $applications = append $applications (merge (dict "name" $name) (deepCopy $application)) }}
{{- end }}
{{- end }}
{{- end }}
{{- range $applications }}
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: sx-{{ .name }}
spec:
  source:
    repoURL: {{ $.Values.default.repoURL }}
    targetRevision: {{ $.Values.default.targetRevision }}
    path: platform-apps/charts/{{ .name }}
    helm:
      {{- with .helmOptions }}
        {{- toYaml . | nindent 6 }}
      {{- end }}
      valueFiles:
        {{- toYaml $.Values.default.valueFiles | nindent 6}}
        {{- with .valueFiles }}
        {{- toYaml . | nindent 6}}
        {{- end }}
      ignoreMissingValueFiles: true
      parameters:
        - name: kubriXPlan
          value: {{ $.Values.kubriXPlan }}
        - name: global.repoURL
          value: {{ $.Values.default.repoURL }}
---
{{- end }}
`

// TargetChart writes a small kubriX platform repository into dir: a target chart like kubriX's, a map-format
// defaults file installing grafana and loki, a bootstrap Application for target type "small" that layers
// values-small.yaml over the defaults, and the charts grafana (named sx-grafana) and loki with default values.
// Tests add or overwrite files with Write.
func TargetChart(t *testing.T, dir string) {
	t.Helper()
	Write(t, dir, map[string]string{
		"platform-apps/target-chart/Chart.yaml":                   "apiVersion: v2\nname: platform-apps-chart\nversion: 0.1.0\n",
		"platform-apps/target-chart/templates/application.yaml":   targetChartApplication,
		"platform-apps/target-chart/values.yaml":                  "default:\n  repoURL: https://example.com/repo\n  targetRevision: main\n  valueFiles: []\nkubriXPlan: oss\napplicationMap: false\napplications: ~\n",
		"platform-apps/target-chart/values-kubrix-default.yaml":   "applicationMap: true\ndefault:\n  valueFiles:\n  - values-kubrix-default.yaml\napplications:\n  grafana:\n    enabled: true\n  loki:\n    enabled: true\n  tempo:\n    enabled: false\n",
		"platform-apps/target-chart/values-small.yaml":            "default:\n  valueFiles:\n  - values-kubrix-default.yaml\n  - values-size-small.yaml\n",
		"bootstrap-app-small.yaml":                                BootstrapApp("values-kubrix-default.yaml", "values-small.yaml", "values-customer.yaml"),
		"platform-apps/charts/grafana/Chart.yaml":                 "apiVersion: v2\nname: sx-grafana\nversion: 0.1.0\n",
		"platform-apps/charts/grafana/values-kubrix-default.yaml": "grafana:\n  replicas: 1\n  ingress:\n    enabled: true\n",
		"platform-apps/charts/grafana/values-size-small.yaml":     "grafana:\n  replicas: 2\n",
		"platform-apps/charts/loki/Chart.yaml":                    "apiVersion: v2\nname: loki\nversion: 0.1.0\n",
		"platform-apps/charts/loki/values-kubrix-default.yaml":    "loki:\n  retention: 7d\n",
	})
}

// BootstrapApp is a bootstrap Application like kubriX's bootstrap-app-<type>.yaml listing the given target values files.
func BootstrapApp(valueFiles ...string) string {
	s := "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: sx-bootstrap-app\nspec:\n  source:\n" +
		"    repoURL: https://example.com/bootstrap\n    targetRevision: main\n    path: platform-apps/target-chart\n    helm:\n" +
		"      ignoreMissingValueFiles: true\n      valueFiles:\n"
	for _, f := range valueFiles {
		s += "        - " + f + "\n"
	}
	return s + "      parameters:\n        - name: default.repoURL\n          value: $ARGOCD_APP_SOURCE_REPO_URL\n"
}
