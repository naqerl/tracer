package install

import (
	"bytes"
	"strings"
	"testing"
)

func TestServiceTemplateKubeconfig(t *testing.T) {
	tests := []struct {
		name    string
		kubecfg string
		want    string
		notWant string
	}{
		{
			name:    "with_kubeconfig",
			kubecfg: "/etc/tracer/kubeconfig",
			want:    "Environment=KUBECONFIG=/etc/tracer/kubeconfig\n",
		},
		{
			name:    "without_kubeconfig",
			kubecfg: "",
			notWant: "KUBECONFIG",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			data := serviceData{
				ConfigFilePath: ConfigFilePath,
				Bindir:         "/usr/local/bin",
				User:           "tracer-collector",
				Group:          "tracer",
				Kubeconfig:     tt.kubecfg,
			}
			if err := serviceTmpl.Execute(&buf, data); err != nil {
				t.Fatalf("could not execute template: %v", err)
			}
			got := buf.String()

			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("unit missing %q:\n%s", tt.want, got)
			}
			if tt.notWant != "" && strings.Contains(got, tt.notWant) {
				t.Errorf("unit unexpectedly contains %q:\n%s", tt.notWant, got)
			}
			if !strings.Contains(got, "ExecStart=/usr/local/bin/tracer-collector") {
				t.Errorf("unit missing ExecStart:\n%s", got)
			}
			// The Environment line must sit on its own line, not glued to the
			// neighbouring directive by template whitespace trimming.
			if tt.kubecfg != "" && !strings.Contains(got, "adm\nEnvironment=KUBECONFIG") {
				t.Errorf("Environment line not separated from SupplementaryGroups:\n%s", got)
			}
		})
	}
}
