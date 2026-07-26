package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validConfig = `
schedule: "0 3 * * *"
targets:
  local:
    repository: /backups/local
    password_file: /run/secrets/restic_local
    retention:
      keep_daily: 30
  c2:
    repository: s3:https://eu-002.s3.synologyc2.net/bucket
    password: geheim
    env:
      AWS_ACCESS_KEY_ID: ${TEST_S3_KEY}
    retention:
      keep_last: 1
`

func TestLoadValid(t *testing.T) {
	t.Setenv("TEST_S3_KEY", "mykey")
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LabelPrefix != "stack-backup" {
		t.Errorf("Default-Prefix erwartet, bekommen %q", cfg.LabelPrefix)
	}
	if cfg.StopTimeout != 30 {
		t.Errorf("Default-StopTimeout erwartet, bekommen %d", cfg.StopTimeout)
	}
	if !reflect.DeepEqual(cfg.TargetNames(), []string{"c2", "local"}) {
		t.Errorf("TargetNames falsch: %v", cfg.TargetNames())
	}

	env := cfg.Targets["c2"].ResticEnv()
	want := []string{
		"RESTIC_REPOSITORY=s3:https://eu-002.s3.synologyc2.net/bucket",
		"RESTIC_PASSWORD=geheim",
		"AWS_ACCESS_KEY_ID=mykey",
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("ResticEnv falsch:\n got %v\nwant %v", env, want)
	}
}

func TestRetentionFlags(t *testing.T) {
	r := Retention{KeepDaily: 30, KeepLast: 1}
	want := []string{"--keep-last", "1", "--keep-daily", "30"}
	if got := r.Flags(); !reflect.DeepEqual(got, want) {
		t.Errorf("Flags falsch: %v", got)
	}
	if !(Retention{}).IsZero() {
		t.Error("leere Retention muss IsZero sein")
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"schedule fehlt": `
targets:
  local: {repository: /r, password: x}
`,
		"mindestens ein Target": `
schedule: "0 3 * * *"
`,
		"ohne repository": `
schedule: "0 3 * * *"
targets:
  local: {password: x}
`,
		"ohne password": `
schedule: "0 3 * * *"
targets:
  local: {repository: /r}
`,
	}
	for wantErr, content := range cases {
		_, err := Load(writeConfig(t, content))
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("erwartet Fehler mit %q, bekommen %v", wantErr, err)
		}
	}
}

func TestPasswordViaEnvMapIsAccepted(t *testing.T) {
	cfg := `
schedule: "0 3 * * *"
targets:
  local:
    repository: /r
    env:
      RESTIC_PASSWORD: ${MY_SECRET}
`
	if _, err := Load(writeConfig(t, cfg)); err != nil {
		t.Errorf("Passwort via env-Map muss akzeptiert werden: %v", err)
	}
}
