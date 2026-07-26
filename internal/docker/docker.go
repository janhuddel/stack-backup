// Package docker kapselt den Zugriff auf die Docker-API: Discovery der zu
// sichernden Container, Exec-Streaming sowie Stop/Start.
package docker

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// Mount beschreibt einen Mount eines Containers aus Backup-Sicht.
type Mount struct {
	// Type ist "bind" oder "volume".
	Type string
	// VolumeName ist bei benannten Volumes gesetzt, bei Bind-Mounts leer.
	VolumeName string
	// Source ist der Host-Pfad (bei Volumes der Mountpoint unter /var/lib/docker/volumes).
	Source string
	// Destination ist der Pfad im Container — das primäre Identifikationsmerkmal.
	Destination string
	// VolumeLabels sind die Labels des benannten Volumes (leer bei Bind-Mounts).
	VolumeLabels map[string]string
}

// Container ist ein laufender Container mit Backup-Label.
type Container struct {
	ID     string
	Name   string
	Labels map[string]string
	Mounts []Mount
}

// Skipped ist ein laufender Container ohne aktiviertes Backup.
type Skipped struct {
	Name string
	// HasConfigLabels meldet, ob der Container zwar Backup-Labels trägt,
	// aber kein "<prefix>.enable=true" — vermutlich eine Fehlkonfiguration.
	HasConfigLabels bool
}

// classify ordnet einen Container anhand seiner Labels ein: Backup aktiviert,
// bzw. ob überhaupt Labels mit dem Backup-Prefix vorhanden sind.
func classify(labels map[string]string, labelPrefix string) (enabled, hasConfig bool) {
	if labels[labelPrefix+".enable"] == "true" {
		return true, true
	}
	for key := range labels {
		if strings.HasPrefix(key, labelPrefix+".") {
			return false, true
		}
	}
	return false, false
}

// HealthOK meldet, ob ein Backup laut Health-Status starten darf.
// Kein Healthcheck definiert ("" bzw. "none") gilt als OK.
func HealthOK(status string) bool {
	return status == "" || status == container.NoHealthcheck || status == container.Healthy
}

// Client ist ein dünner Wrapper um den Docker-SDK-Client.
type Client struct {
	api *client.Client
	// selfID ist die eigene Container-ID, sofern ermittelbar (siehe self.go).
	selfID string
}

// NewClient verbindet sich anhand des Standard-Envs (DOCKER_HOST etc.).
func NewClient() (*Client, error) {
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker-client: %w", err)
	}
	return &Client{api: api, selfID: selfContainerID()}, nil
}

// SelfID liefert die ermittelte eigene Container-ID ("" = nicht ermittelbar).
func (c *Client) SelfID() string { return c.selfID }

// Close gibt die Verbindung frei.
func (c *Client) Close() error { return c.api.Close() }

// ListBackupContainers liest alle laufenden Container ein und liefert die mit
// "<prefix>.enable=true" inklusive Mounts und Volume-Labels; alle übrigen
// werden als Skipped zurückgegeben, damit der Aufrufer sie loggen kann.
func (c *Client) ListBackupContainers(ctx context.Context, labelPrefix string) ([]Container, []Skipped, error) {
	list, err := c.api.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("container listen: %w", err)
	}

	result := make([]Container, 0, len(list))
	var skipped []Skipped
	for _, item := range list {
		if isSelf(item.ID, c.selfID) {
			// Der eigene Container ist nie Backup-Objekt — und taucht auch
			// nicht als "ohne Backup-Label" im Log auf.
			continue
		}
		enabled, hasConfig := classify(item.Labels, labelPrefix)
		if !enabled {
			skipped = append(skipped, Skipped{
				Name:            containerName(item.Names, item.ID),
				HasConfigLabels: hasConfig,
			})
			continue
		}
		ct := Container{
			ID:     item.ID,
			Name:   containerName(item.Names, item.ID),
			Labels: item.Labels,
		}
		// Mounts aus ContainerInspect statt aus der Liste: Docker Desktop
		// meldet in der Liste VM-interne Pfade, im Inspect die echten
		// Host-Pfade. Auf nativem Docker sind beide identisch.
		inspect, err := c.api.ContainerInspect(ctx, item.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("container %s inspizieren: %w", containerName(item.Names, item.ID), err)
		}
		for _, m := range inspect.Mounts {
			switch m.Type {
			case "bind", "volume":
			default:
				continue
			}
			mount := Mount{
				Type:        string(m.Type),
				VolumeName:  m.Name,
				Source:      m.Source,
				Destination: m.Destination,
			}
			if mount.Type == "volume" && m.Name != "" {
				if vol, err := c.api.VolumeInspect(ctx, m.Name); err == nil {
					mount.VolumeLabels = vol.Labels
				}
			}
			ct.Mounts = append(ct.Mounts, mount)
		}
		result = append(result, ct)
	}
	return result, skipped, nil
}

func containerName(names []string, id string) string {
	if len(names) > 0 {
		return strings.TrimPrefix(names[0], "/")
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Health liefert den aktuellen Healthcheck-Status des Containers
// ("starting", "healthy", "unhealthy"). "" bedeutet: kein Healthcheck definiert.
func (c *Client) Health(ctx context.Context, containerID string) (string, error) {
	inspect, err := c.api.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("container inspizieren: %w", err)
	}
	if inspect.State == nil || inspect.State.Health == nil {
		return "", nil
	}
	return inspect.State.Health.Status, nil
}

// Exec führt command via "sh -c" im Container aus. stdout des Kommandos wird
// nach stdout geschrieben (nil = verwerfen), stderr nach stderr. Ein Exit-Code
// ungleich 0 führt zu einem Fehler.
func (c *Client) Exec(ctx context.Context, containerID, command string, stdout, stderr io.Writer) error {
	exec, err := c.api.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec anlegen: %w", err)
	}

	resp, err := c.api.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if _, err := stdcopy.StdCopy(stdout, stderr, resp.Reader); err != nil {
		return fmt.Errorf("exec stream: %w", err)
	}

	inspect, err := c.api.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("exec %q: exit code %d", command, inspect.ExitCode)
	}
	return nil
}

// Stop hält den Container mit dem angegebenen Timeout (Sekunden) an.
func (c *Client) Stop(ctx context.Context, containerID string, timeoutSeconds int) error {
	return c.api.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeoutSeconds})
}

// Start startet den Container wieder.
func (c *Client) Start(ctx context.Context, containerID string) error {
	return c.api.ContainerStart(ctx, containerID, container.StartOptions{})
}
