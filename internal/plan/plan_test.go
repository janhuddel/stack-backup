package plan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/janhuddel/stack-backup/internal/docker"
)

const prefix = "stack-backup"

var allTargets = []string{"c2", "local"}

func labels(kv ...string) map[string]string {
	m := map[string]string{prefix + ".enable": "true"}
	for i := 0; i < len(kv); i += 2 {
		m[prefix+"."+kv[i]] = kv[i+1]
	}
	return m
}

func TestExecJob(t *testing.T) {
	ct := docker.Container{
		ID:   "abc",
		Name: "postgres",
		Labels: labels(
			"exec.command", "pg_dump -U postgres mydb",
			"exec.filename", "mydb.sql",
		),
	}
	plans, warnings := Build([]docker.Container{ct}, prefix, allTargets)
	if len(warnings) != 0 {
		t.Fatalf("unerwartete Warnungen: %v", warnings)
	}
	if len(plans) != 1 {
		t.Fatalf("erwartet 1 Plan, bekommen %d", len(plans))
	}
	p := plans[0]
	if p.Exec == nil || p.Exec.Command != "pg_dump -U postgres mydb" || p.Exec.Filename != "mydb.sql" {
		t.Errorf("exec-Job falsch: %+v", p.Exec)
	}
	if !reflect.DeepEqual(p.Targets, allTargets) {
		t.Errorf("erwartet alle Targets, bekommen %v", p.Targets)
	}
}

func TestExecFilenameDefault(t *testing.T) {
	ct := docker.Container{Name: "n8n", Labels: labels("exec.command", "true")}
	plans, _ := Build([]docker.Container{ct}, prefix, allTargets)
	if got := plans[0].Exec.Filename; got != "n8n.dump" {
		t.Errorf("erwartet Default-Filename n8n.dump, bekommen %q", got)
	}
}

func TestVolumesAllWithBindAndNamed(t *testing.T) {
	ct := docker.Container{
		Name:   "vaultwarden",
		Labels: labels("volumes", "all"),
		Mounts: []docker.Mount{
			{Type: "bind", Source: "/mnt/docker-nfs-01/volumes/13-applications/vaultwarden/data", Destination: "/data"},
			{Type: "volume", VolumeName: "vw_cache", Source: "/var/lib/docker/volumes/vw_cache/_data", Destination: "/cache"},
		},
	}
	plans, warnings := Build([]docker.Container{ct}, prefix, allTargets)
	if len(warnings) != 0 {
		t.Fatalf("unerwartete Warnungen: %v", warnings)
	}
	if len(plans[0].Mounts) != 2 {
		t.Fatalf("erwartet 2 Mounts, bekommen %+v", plans[0].Mounts)
	}
}

func TestVolumeSelectionByDestinationAndName(t *testing.T) {
	ct := docker.Container{
		Name:   "app",
		Labels: labels("volumes", "/data, appvol"),
		Mounts: []docker.Mount{
			{Type: "bind", Source: "/mnt/nfs/app/data", Destination: "/data"},
			{Type: "volume", VolumeName: "appvol", Source: "/var/lib/docker/volumes/appvol/_data", Destination: "/config"},
			{Type: "bind", Source: "/mnt/nfs/app/tmp", Destination: "/tmp-data"},
		},
	}
	plans, warnings := Build([]docker.Container{ct}, prefix, allTargets)
	if len(warnings) != 0 {
		t.Fatalf("unerwartete Warnungen: %v", warnings)
	}
	mounts := plans[0].Mounts
	if len(mounts) != 2 || mounts[0].Destination != "/data" || mounts[1].VolumeName != "appvol" {
		t.Errorf("Selektion falsch: %+v", mounts)
	}
}

func TestVolumeSelectorWithoutMatchWarns(t *testing.T) {
	ct := docker.Container{Name: "app", Labels: labels("volumes", "/nope")}
	plans, warnings := Build([]docker.Container{ct}, prefix, allTargets)
	if len(plans) != 0 {
		t.Errorf("erwartet keinen Plan, bekommen %+v", plans)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "/nope") {
		t.Errorf("erwartet Warnung zu /nope, bekommen %v", warnings)
	}
}

func TestExcludesPerMountLabelWinsOverVolumeLabel(t *testing.T) {
	ct := docker.Container{
		Name: "app",
		Labels: labels(
			"volumes", "all",
			"volume./data.exclude", "cache/**, tmp/**",
		),
		Mounts: []docker.Mount{
			{
				Type: "volume", VolumeName: "data", Destination: "/data",
				Source:       "/var/lib/docker/volumes/data/_data",
				VolumeLabels: map[string]string{prefix + ".exclude": "vom-volume/**"},
			},
			{
				Type: "volume", VolumeName: "logs", Destination: "/logs",
				Source:       "/var/lib/docker/volumes/logs/_data",
				VolumeLabels: map[string]string{prefix + ".exclude": "*.log"},
			},
		},
	}
	plans, _ := Build([]docker.Container{ct}, prefix, allTargets)
	mounts := plans[0].Mounts
	if !reflect.DeepEqual(mounts[0].Excludes, []string{"cache/**", "tmp/**"}) {
		t.Errorf("Container-Label muss gewinnen, bekommen %v", mounts[0].Excludes)
	}
	if !reflect.DeepEqual(mounts[1].Excludes, []string{"*.log"}) {
		t.Errorf("Volume-Label muss greifen, bekommen %v", mounts[1].Excludes)
	}
}

func TestVolumeLabelEnableFalseSkipsMount(t *testing.T) {
	ct := docker.Container{
		Name:   "app",
		Labels: labels("volumes", "all"),
		Mounts: []docker.Mount{
			{
				Type: "volume", VolumeName: "skipme", Destination: "/skip",
				Source:       "/var/lib/docker/volumes/skipme/_data",
				VolumeLabels: map[string]string{prefix + ".enable": "false"},
			},
			{Type: "bind", Source: "/mnt/nfs/data", Destination: "/data"},
		},
	}
	plans, _ := Build([]docker.Container{ct}, prefix, allTargets)
	mounts := plans[0].Mounts
	if len(mounts) != 1 || mounts[0].Destination != "/data" {
		t.Errorf("Mount mit enable=false muss übersprungen werden: %+v", mounts)
	}
}

func TestStopPrePostAndTargets(t *testing.T) {
	ct := docker.Container{
		Name: "minecraft",
		Labels: labels(
			"volumes", "all",
			"stop", "true",
			"pre-command", "rcon-cli save-off",
			"post-command", "rcon-cli save-on",
			"targets", "local",
		),
		Mounts: []docker.Mount{{Type: "bind", Source: "/mnt/nfs/mc", Destination: "/data"}},
	}
	plans, _ := Build([]docker.Container{ct}, prefix, allTargets)
	p := plans[0]
	if !p.Stop || p.PreCommand != "rcon-cli save-off" || p.PostCommand != "rcon-cli save-on" {
		t.Errorf("stop/pre/post falsch: %+v", p)
	}
	if !reflect.DeepEqual(p.Targets, []string{"local"}) {
		t.Errorf("Target-Auswahl falsch: %v", p.Targets)
	}
}

func TestUnknownTargetSkipsContainer(t *testing.T) {
	ct := docker.Container{
		Name:   "app",
		Labels: labels("volumes", "all", "targets", "gibtsnicht"),
		Mounts: []docker.Mount{{Type: "bind", Source: "/mnt/nfs/x", Destination: "/data"}},
	}
	plans, warnings := Build([]docker.Container{ct}, prefix, allTargets)
	if len(plans) != 0 {
		t.Errorf("Container ohne gültiges Target muss übersprungen werden: %+v", plans)
	}
	if len(warnings) != 2 {
		t.Errorf("erwartet 2 Warnungen (unbekanntes Target + übersprungen), bekommen %v", warnings)
	}
}

func TestEnableWithoutWorkWarns(t *testing.T) {
	ct := docker.Container{Name: "leer", Labels: labels()}
	plans, warnings := Build([]docker.Container{ct}, prefix, allTargets)
	if len(plans) != 0 {
		t.Errorf("erwartet keinen Plan: %+v", plans)
	}
	if len(warnings) != 1 {
		t.Errorf("erwartet Hinweis-Warnung, bekommen %v", warnings)
	}
}
