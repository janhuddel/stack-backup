// Package runner orchestriert eine Backup-Iteration: Discovery, Plan,
// Ausführung der Jobs pro Container und Retention pro Target.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/janhuddel/stack-backup/internal/config"
	"github.com/janhuddel/stack-backup/internal/docker"
	"github.com/janhuddel/stack-backup/internal/plan"
	"github.com/janhuddel/stack-backup/internal/restic"
)

// Runner führt Backup-Iterationen aus. Läufe sind gegeneinander serialisiert:
// startet der Cron eine Iteration, während die vorige noch läuft, wird sie
// übersprungen.
type Runner struct {
	Cfg    *config.Config
	Docker *docker.Client
	Log    *slog.Logger

	mu sync.Mutex
}

// New erzeugt einen Runner.
func New(cfg *config.Config, dc *docker.Client, log *slog.Logger) *Runner {
	return &Runner{Cfg: cfg, Docker: dc, Log: log}
}

// TryRun führt eine Iteration aus, sofern keine andere läuft.
func (r *Runner) TryRun(ctx context.Context) {
	if !r.mu.TryLock() {
		r.Log.Warn("vorherige Iteration läuft noch, überspringe diesen Lauf")
		return
	}
	defer r.mu.Unlock()
	if err := r.run(ctx); err != nil {
		r.Log.Error("Backup-Iteration mit Fehlern beendet", "error", err)
	}
}

// RunOnce führt genau eine Iteration aus (für --once und Tests).
func (r *Runner) RunOnce(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run(ctx)
}

func (r *Runner) run(ctx context.Context) error {
	r.Log.Info("Backup-Iteration gestartet")

	containers, skipped, err := r.Docker.ListBackupContainers(ctx, r.Cfg.LabelPrefix)
	if err != nil {
		return err
	}
	var ignoredNames []string
	for _, s := range skipped {
		if s.HasConfigLabels {
			// Backup-Labels vorhanden, aber enable != true — vermutlich vergessen.
			r.Log.Warn("Container hat Backup-Labels, aber kein enable=true — übersprungen", "container", s.Name)
		} else {
			ignoredNames = append(ignoredNames, s.Name)
		}
	}
	if len(ignoredNames) > 0 {
		r.Log.Info("Container ohne Backup-Label ignoriert", "count", len(ignoredNames), "container", strings.Join(ignoredNames, ", "))
	}

	plans, warnings := plan.Build(containers, r.Cfg.LabelPrefix, r.Cfg.TargetNames())
	for _, w := range warnings {
		r.Log.Warn(w)
	}
	if len(plans) == 0 {
		r.Log.Info("keine Container mit Backup-Konfiguration gefunden")
		return nil
	}

	// Repositories einmalig initialisieren.
	clients := make(map[string]*restic.Client, len(r.Cfg.Targets))
	var errs []error
	for name, target := range r.Cfg.Targets {
		rc := restic.New(name, target, r.Log)
		if err := rc.EnsureInit(ctx); err != nil {
			errs = append(errs, fmt.Errorf("target %s: %w", name, err))
			continue
		}
		clients[name] = rc
	}

	ok, failed := 0, 0
	for _, p := range plans {
		if err := r.runContainer(ctx, p, clients); err != nil {
			errs = append(errs, fmt.Errorf("container %s: %w", p.ContainerName, err))
			failed++
		} else {
			ok++
		}
	}

	// Retention pro Target — auch wenn einzelne Jobs fehlgeschlagen sind.
	for _, name := range r.Cfg.TargetNames() {
		rc, ready := clients[name]
		if !ready {
			continue
		}
		if err := rc.Forget(ctx); err != nil {
			errs = append(errs, fmt.Errorf("target %s: %w", name, err))
		}
	}

	r.Log.Info("Backup-Iteration beendet", "container_ok", ok, "container_failed", failed)
	return errors.Join(errs...)
}

// runContainer arbeitet den Plan eines Containers ab: erst das Exec-Backup im
// laufenden Container, dann die Mount-Backups (mit Stop bzw. Pre/Post-Hooks).
func (r *Runner) runContainer(ctx context.Context, p plan.ContainerPlan, clients map[string]*restic.Client) error {
	log := r.Log.With("container", p.ContainerName)
	var errs []error

	if p.Exec != nil {
		for _, target := range p.Targets {
			rc, ready := clients[target]
			if !ready {
				continue
			}
			log.Info("exec-Backup", "target", target, "filename", p.Exec.Filename)
			if err := r.execBackup(ctx, p, rc); err != nil {
				errs = append(errs, fmt.Errorf("exec-backup (target %s): %w", target, err))
			}
		}
	}

	if len(p.Mounts) > 0 {
		if err := r.mountBackups(ctx, p, clients, log); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// execBackup streamt stdout des Container-Kommandos direkt in
// "restic backup --stdin"; stderr des Kommandos landet in den Logs.
func (r *Runner) execBackup(ctx context.Context, p plan.ContainerPlan, rc *restic.Client) error {
	tags := []string{"container=" + p.ContainerName, "type=exec"}
	stdin, wait, err := rc.BackupStdin(ctx, p.Exec.Filename, tags)
	if err != nil {
		return err
	}

	logWriter := newLogWriter(r.Log.With("container", p.ContainerName, "stream", "exec-stderr"))
	execErr := r.Docker.Exec(ctx, p.ContainerID, p.Exec.Command, stdin, logWriter)
	logWriter.Flush()
	closeErr := stdin.Close()
	waitErr := wait()

	// Der Exec-Fehler hat Vorrang: ein fehlgeschlagener Dump darf nie als
	// erfolgreiches Backup gewertet werden, selbst wenn restic sauber beendet.
	return errors.Join(execErr, closeErr, waitErr)
}

// mountBackups sichert alle Mounts des Plans in alle Targets. Der Container
// wird dafür höchstens einmal gestoppt bzw. Pre/Post-Hooks höchstens einmal
// ausgeführt — unabhängig von der Anzahl Mounts und Targets.
func (r *Runner) mountBackups(ctx context.Context, p plan.ContainerPlan, clients map[string]*restic.Client, log *slog.Logger) (err error) {
	if p.PreCommand != "" {
		log.Info("pre-command", "command", p.PreCommand)
		logWriter := newLogWriter(log.With("stream", "pre-command"))
		if preErr := r.Docker.Exec(ctx, p.ContainerID, p.PreCommand, logWriter, logWriter); preErr != nil {
			return fmt.Errorf("pre-command: %w", preErr)
		}
		logWriter.Flush()
	}
	if p.PostCommand != "" {
		// Post-Hook läuft garantiert, auch wenn Backups fehlschlagen.
		defer func() {
			log.Info("post-command", "command", p.PostCommand)
			logWriter := newLogWriter(log.With("stream", "post-command"))
			if postErr := r.Docker.Exec(ctx, p.ContainerID, p.PostCommand, logWriter, logWriter); postErr != nil {
				err = errors.Join(err, fmt.Errorf("post-command: %w", postErr))
			}
			logWriter.Flush()
		}()
	}

	if p.Stop {
		log.Info("stoppe Container für konsistentes Backup")
		if stopErr := r.Docker.Stop(ctx, p.ContainerID, r.Cfg.StopTimeout); stopErr != nil {
			return fmt.Errorf("container stoppen: %w", stopErr)
		}
		// Neustart garantiert — auch bei Fehlern oder Panic während des Backups.
		defer func() {
			log.Info("starte Container wieder")
			if startErr := r.Docker.Start(ctx, p.ContainerID); startErr != nil {
				err = errors.Join(err, fmt.Errorf("container starten: %w", startErr))
			}
		}()
	}

	var errs []error
	for _, m := range p.Mounts {
		if _, statErr := os.Stat(m.Source); statErr != nil {
			errs = append(errs, fmt.Errorf("mount %s: Quellpfad %s nicht im Backup-Container verfügbar (Volume-Root mounten!): %w",
				m.Destination, m.Source, statErr))
			continue
		}
		tags := []string{"container=" + p.ContainerName, "type=volume", "mount=" + m.Destination}
		for _, target := range p.Targets {
			rc, ready := clients[target]
			if !ready {
				continue
			}
			log.Info("mount-Backup", "target", target, "mount", m.Destination, "source", m.Source)
			if backupErr := rc.Backup(ctx, m.Source, tags, m.Excludes); backupErr != nil {
				errs = append(errs, fmt.Errorf("mount %s (target %s): %w", m.Destination, target, backupErr))
			}
		}
	}
	return errors.Join(errs...)
}
