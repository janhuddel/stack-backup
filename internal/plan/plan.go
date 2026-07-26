// Package plan übersetzt Container-/Volume-Labels in einen Backup-Plan.
// Die Logik ist bewusst frei von Docker- und restic-Abhängigkeiten, damit sie
// sich isoliert testen lässt.
package plan

import (
	"fmt"
	"slices"
	"strings"

	"github.com/janhuddel/stack-backup/internal/docker"
)

// ExecJob ist ein Backup per Kommando im laufenden Container.
type ExecJob struct {
	// Command wird via "sh -c" ausgeführt; stdout fließt in den Snapshot.
	Command string
	// Filename ist der Dateiname im Snapshot (--stdin-filename).
	Filename string
}

// MountJob ist ein Backup eines Bind-Mounts oder benannten Volumes.
type MountJob struct {
	// Destination ist der Pfad im Container (Identifikationsmerkmal).
	Destination string
	// Source ist der Host-Pfad, den restic sichert.
	Source string
	// VolumeName ist bei benannten Volumes gesetzt.
	VolumeName string
	// Excludes sind restic --exclude-Patterns.
	Excludes []string
}

// ContainerPlan bündelt alle Backup-Jobs eines Containers.
type ContainerPlan struct {
	ContainerID   string
	ContainerName string
	// Stop verlangt, den Container für die Mount-Backups anzuhalten.
	Stop bool
	// PreCommand/PostCommand laufen vor bzw. nach den Mount-Backups im Container.
	PreCommand  string
	PostCommand string
	// Targets sind die Namen der Targets, in die gesichert wird.
	Targets []string
	Exec    *ExecJob
	Mounts  []MountJob
}

// HasWork meldet, ob der Plan überhaupt etwas zu sichern hat.
func (p ContainerPlan) HasWork() bool {
	return p.Exec != nil || len(p.Mounts) > 0
}

// Build erstellt aus den gefundenen Containern die Backup-Pläne.
// allTargets ist die Liste der konfigurierten Target-Namen; Warnungen (z.B.
// unbekannte Targets oder Selektoren ohne Treffer) werden gesammelt
// zurückgegeben und brechen den Lauf nicht ab.
func Build(containers []docker.Container, labelPrefix string, allTargets []string) ([]ContainerPlan, []string) {
	var plans []ContainerPlan
	var warnings []string

	for _, ct := range containers {
		p, w := buildOne(ct, labelPrefix+".", allTargets)
		warnings = append(warnings, w...)
		if p.HasWork() {
			plans = append(plans, p)
		}
	}
	return plans, warnings
}

func buildOne(ct docker.Container, prefix string, allTargets []string) (ContainerPlan, []string) {
	var warnings []string
	label := func(key string) string { return strings.TrimSpace(ct.Labels[prefix+key]) }

	p := ContainerPlan{
		ContainerID:   ct.ID,
		ContainerName: ct.Name,
		Stop:          label("stop") == "true",
		PreCommand:    label("pre-command"),
		PostCommand:   label("post-command"),
	}

	// Targets auflösen: Default alle, sonst kommagetrennte Auswahl.
	if raw := label("targets"); raw != "" {
		for _, name := range splitList(raw) {
			if slices.Contains(allTargets, name) {
				p.Targets = append(p.Targets, name)
			} else {
				warnings = append(warnings, fmt.Sprintf("container %s: unbekanntes Target %q ignoriert", ct.Name, name))
			}
		}
		if len(p.Targets) == 0 {
			warnings = append(warnings, fmt.Sprintf("container %s: kein gültiges Target übrig, Container übersprungen", ct.Name))
			return ContainerPlan{}, warnings
		}
	} else {
		p.Targets = slices.Clone(allTargets)
	}

	// Exec-Backup.
	if cmd := label("exec.command"); cmd != "" {
		filename := label("exec.filename")
		if filename == "" {
			filename = ct.Name + ".dump"
		}
		p.Exec = &ExecJob{Command: cmd, Filename: filename}
	}

	// Mount-Backups.
	if raw := label("volumes"); raw != "" {
		p.Mounts = selectMounts(ct, raw, prefix, &warnings)
	}

	if !p.HasWork() {
		warnings = append(warnings, fmt.Sprintf("container %s: enable=true, aber weder exec.command noch volumes gesetzt", ct.Name))
	}
	return p, warnings
}

// selectMounts wertet das volumes-Label aus ("all" oder Liste von
// Container-Pfaden bzw. Volume-Namen) und sammelt die Excludes ein.
func selectMounts(ct docker.Container, raw, prefix string, warnings *[]string) []MountJob {
	var jobs []MountJob

	appendMount := func(m docker.Mount) {
		// Volume-Label enable=false überspringt den Mount explizit.
		if m.VolumeLabels[strings.TrimSuffix(prefix, ".")+".enable"] == "false" {
			return
		}
		jobs = append(jobs, MountJob{
			Destination: m.Destination,
			Source:      m.Source,
			VolumeName:  m.VolumeName,
			Excludes:    mountExcludes(ct, m, prefix),
		})
	}

	if strings.TrimSpace(raw) == "all" {
		for _, m := range ct.Mounts {
			appendMount(m)
		}
		if len(jobs) == 0 {
			*warnings = append(*warnings, fmt.Sprintf("container %s: volumes=all, aber keine Mounts gefunden", ct.Name))
		}
		return jobs
	}

	for _, sel := range splitList(raw) {
		idx := slices.IndexFunc(ct.Mounts, func(m docker.Mount) bool {
			return m.Destination == sel || (m.VolumeName != "" && m.VolumeName == sel)
		})
		if idx < 0 {
			*warnings = append(*warnings, fmt.Sprintf("container %s: kein Mount passt zu %q", ct.Name, sel))
			continue
		}
		appendMount(ct.Mounts[idx])
	}
	return jobs
}

// mountExcludes ermittelt die Excludes eines Mounts. Das Per-Mount-Label am
// Container ("<prefix>volume.<destination>.exclude") gewinnt gegenüber dem
// Label am benannten Volume ("<prefix ohne Punkt>.exclude").
func mountExcludes(ct docker.Container, m docker.Mount, prefix string) []string {
	if raw, ok := ct.Labels[prefix+"volume."+m.Destination+".exclude"]; ok {
		return splitList(raw)
	}
	if raw, ok := m.VolumeLabels[strings.TrimSuffix(prefix, ".")+".exclude"]; ok {
		return splitList(raw)
	}
	return nil
}

func splitList(raw string) []string {
	var result []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			result = append(result, p)
		}
	}
	return result
}
