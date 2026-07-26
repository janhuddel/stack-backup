package docker

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name        string
		labels      map[string]string
		wantEnabled bool
		wantConfig  bool
	}{
		{"enable=true", map[string]string{"stack-backup.enable": "true"}, true, true},
		{"enable=false", map[string]string{"stack-backup.enable": "false"}, false, true},
		{"labels ohne enable", map[string]string{"stack-backup.volumes": "all"}, false, true},
		{"keine backup-labels", map[string]string{"com.docker.compose.project": "x"}, false, false},
		{"gar keine labels", nil, false, false},
		{"fremdes prefix nicht verwechseln", map[string]string{"stack-backup2.enable": "true"}, false, false},
	}
	for _, tc := range cases {
		enabled, hasConfig := classify(tc.labels, "stack-backup")
		if enabled != tc.wantEnabled || hasConfig != tc.wantConfig {
			t.Errorf("%s: classify = (%v, %v), erwartet (%v, %v)",
				tc.name, enabled, hasConfig, tc.wantEnabled, tc.wantConfig)
		}
	}
}
