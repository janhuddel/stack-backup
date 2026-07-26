// Package restic kapselt Aufrufe des restic-CLI-Binaries.
package restic

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/janhuddel/stack-backup/internal/config"
)

// Client führt restic-Kommandos gegen ein konkretes Target aus.
type Client struct {
	TargetName string
	Target     config.Target
	Log        *slog.Logger
}

// New erzeugt einen Client für das benannte Target.
func New(name string, target config.Target, log *slog.Logger) *Client {
	return &Client{TargetName: name, Target: target, Log: log.With("target", name)}
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "restic", args...)
	cmd.Env = append(os.Environ(), c.Target.ResticEnv()...)
	return cmd
}

// run führt restic aus und gibt stderr im Fehlerfall mit zurück.
func (c *Client) run(ctx context.Context, args ...string) error {
	cmd := c.command(ctx, args...)
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		return fmt.Errorf("restic %s: %w: %s", args[0], err, trimmed)
	}
	if trimmed != "" {
		c.Log.Debug("restic output", "cmd", args[0], "output", trimmed)
	}
	return nil
}

// EnsureInit initialisiert das Repository, falls es noch nicht existiert.
func (c *Client) EnsureInit(ctx context.Context) error {
	if err := c.run(ctx, "cat", "config"); err == nil {
		return nil
	}
	c.Log.Info("initialisiere restic-Repository", "repository", c.Target.Repository)
	return c.run(ctx, "init")
}

// Backup sichert einen Pfad mit den angegebenen Tags und Excludes.
func (c *Client) Backup(ctx context.Context, path string, tags []string, excludes []string) error {
	args := []string{"backup", path}
	for _, t := range tags {
		args = append(args, "--tag", t)
	}
	for _, e := range excludes {
		args = append(args, "--exclude", e)
	}
	return c.run(ctx, args...)
}

// BackupStdin sichert einen Datenstrom unter dem angegebenen Dateinamen.
// Der Rückgabewert io.WriteCloser nimmt die Daten entgegen; wait() muss nach
// dem Schließen aufgerufen werden und liefert das Ergebnis des restic-Laufs.
func (c *Client) BackupStdin(ctx context.Context, filename string, tags []string) (io.WriteCloser, func() error, error) {
	args := []string{"backup", "--stdin", "--stdin-filename", filename}
	for _, t := range tags {
		args = append(args, "--tag", t)
	}
	cmd := c.command(ctx, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	wait := func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("restic backup --stdin: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
	return stdin, wait, nil
}

// Forget wendet die Retention-Policy des Targets an und pruned das Repository.
// Gruppiert wird nach Tags und Pfaden, damit jede Backup-Quelle ihre eigene
// Historie behält.
func (c *Client) Forget(ctx context.Context) error {
	policy := c.Target.Retention
	if policy.IsZero() {
		c.Log.Warn("keine Retention konfiguriert, forget übersprungen")
		return nil
	}
	args := append([]string{"forget", "--prune", "--group-by", "tags,paths"}, policy.Flags()...)
	return c.run(ctx, args...)
}

// Passthrough führt beliebige restic-Argumente mit dem Env des Targets aus
// und verbindet stdio direkt — für manuelle Restores/Inspektion.
func (c *Client) Passthrough(ctx context.Context, args []string) error {
	cmd := c.command(ctx, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
