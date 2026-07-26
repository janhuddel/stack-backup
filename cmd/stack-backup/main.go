// stack-backup sichert Docker-Container und deren Mounts per restic.
// Was gesichert wird, konfigurieren die Container selbst über Labels.
//
// Aufrufvarianten:
//
//	stack-backup [--config path]              # Daemon: Cron-gesteuerte Läufe
//	stack-backup [--config path] --once       # genau eine Iteration
//	stack-backup restic --target NAME -- ARGS # restic-Passthrough (Restore etc.)
//
// Der Daemon startet bei SIGUSR1 sofort eine Iteration (manueller Trigger).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/robfig/cron/v3"

	"github.com/janhuddel/stack-backup/internal/config"
	"github.com/janhuddel/stack-backup/internal/docker"
	"github.com/janhuddel/stack-backup/internal/restic"
	"github.com/janhuddel/stack-backup/internal/runner"
)

const defaultConfigPath = "/etc/stack-backup/config.yml"

func main() {
	log := newLogger()
	if err := run(log); err != nil {
		log.Error("abbruch", "error", err)
		os.Exit(1)
	}
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Subcommand "restic": Passthrough mit dem Env eines Targets.
	if len(os.Args) > 1 && os.Args[1] == "restic" {
		return runResticPassthrough(ctx, os.Args[2:], log)
	}

	flags := flag.NewFlagSet("stack-backup", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "Pfad zur Konfigurationsdatei")
	once := flags.Bool("once", false, "genau eine Backup-Iteration ausführen und beenden")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	dc, err := docker.NewClient()
	if err != nil {
		return err
	}
	defer dc.Close()
	if selfID := dc.SelfID(); selfID != "" {
		log.Debug("eigener Container erkannt, wird von der Discovery ausgeschlossen", "id", selfID)
	} else {
		log.Debug("eigene Container-ID nicht ermittelbar — Selbstausschluss inaktiv")
	}

	r := runner.New(cfg, dc, log)

	if *once {
		return r.RunOnce(ctx)
	}

	c := cron.New()
	if _, err := c.AddFunc(cfg.Schedule, func() { r.TryRun(ctx) }); err != nil {
		return fmt.Errorf("ungültiger schedule %q: %w", cfg.Schedule, err)
	}

	// Manueller Trigger: SIGUSR1 stößt sofort eine Iteration an
	// (z.B. "docker kill --signal=USR1 <container>"). Läuft im selben
	// Prozess und ist damit über den Runner-Mutex gegen Cron-Läufe serialisiert.
	trigger := make(chan os.Signal, 1)
	signal.Notify(trigger, syscall.SIGUSR1)

	log.Info("stack-backup gestartet", "schedule", cfg.Schedule, "targets", cfg.TargetNames())
	c.Start()

	for {
		select {
		case <-trigger:
			log.Info("SIGUSR1 empfangen, starte Backup-Iteration")
			r.TryRun(ctx)
		case <-ctx.Done():
			log.Info("beende, warte auf laufende Jobs")
			<-c.Stop().Done()
			return nil
		}
	}
}

// runResticPassthrough führt "stack-backup restic --target NAME [--config path] -- ARGS"
// aus: restic mit fertig aufgebautem Repository-Env des Targets.
func runResticPassthrough(ctx context.Context, args []string, log *slog.Logger) error {
	flags := flag.NewFlagSet("stack-backup restic", flag.ExitOnError)
	configPath := flags.String("config", defaultConfigPath, "Pfad zur Konfigurationsdatei")
	targetName := flags.String("target", "", "Name des Targets aus der Konfiguration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *targetName == "" {
		return fmt.Errorf("restic-passthrough: --target fehlt")
	}
	resticArgs := flags.Args()
	if len(resticArgs) == 0 {
		return fmt.Errorf("restic-passthrough: keine restic-Argumente angegeben")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	target, ok := cfg.Targets[*targetName]
	if !ok {
		return fmt.Errorf("restic-passthrough: unbekanntes Target %q (verfügbar: %s)",
			*targetName, strings.Join(cfg.TargetNames(), ", "))
	}
	return restic.New(*targetName, target, log).Passthrough(ctx, resticArgs)
}
