# stack-backup — Hinweise für Coding-Agents

Label-gesteuertes Backup-Tool für Docker-Stacks (Go + restic als CLI-Subprozess).
Läuft als Container neben dem Stack, liest per Cron alle laufenden Container über
den Docker-Socket und sichert, was per `stack-backup.*`-Label konfiguriert ist.
Fachliche Details und Label-Referenz: [README.md](README.md).

## Sprache & Stil

- Kommentare, Log-Meldungen, Doku und Commit-Messages sind **deutsch** — dabei bleiben.
- Logging über `log/slog` (strukturiert, stdout). Fehler werden pro Job gesammelt
  (`errors.Join`), ein fehlgeschlagener Job bricht den Lauf nie ab.

## Build & Test

```sh
go build ./cmd/stack-backup   # Binary
go vet ./...
go test ./...                 # Unit-Tests (plan, config, docker/classify)
docker build -t stack-backup .   # Multi-Stage: golang:1.25-alpine → alpine + restic
```

Manueller E2E-Test: `test/` enthält einen Example-Stack
(`test/example-stack/docker-compose.yaml`, minio + uptime-kuma mit Labels)
und `test/run-once.sh` (startet `stack-backup:latest --once` mit lokalem
restic-Repository unter `test/backups`). Erst `docker compose up -d` im
example-stack, dann das Skript.

## Architektur (Paket → Verantwortung)

- `cmd/stack-backup` — Flags (`--config`, `--once`), Cron-Daemon mit
  Signal-Handling, Subcommand `restic --target NAME -- ARGS` (Passthrough mit
  fertigem Target-Env für Inspektion/Restore).
- `internal/config` — YAML-Config: Targets, Retention (→ `restic forget`-Flags),
  `${VAR}`-Expansion in `env`-Werten.
- `internal/docker` — SDK-Wrapper: Discovery + Klassifizierung (`classify`),
  Exec-Streaming (stdcopy-Demux, Exit-Code-Prüfung), Stop/Start.
- `internal/plan` — reine Funktion `Build`: Labels → Backup-Plan (Exec-Jobs,
  Mount-Jobs, Targets, Excludes). Neue Label-Logik gehört hierhin (testbar!).
- `internal/runner` — Orchestrierung einer Iteration; Mutex-TryLock gegen
  überlappende Läufe; Stop/Neustart und Post-Hooks per `defer` garantiert.
- `internal/restic` — CLI-Wrapper: `EnsureInit`, `Backup`, `BackupStdin`
  (Streaming-Dumps), `Forget`, `Passthrough`.

## Nicht-offensichtliche Invarianten (nicht brechen!)

- **Mounts immer aus `ContainerInspect`, nie aus `ContainerList`:** Docker
  Desktop meldet in der List-API VM-interne Pfade. Auf nativem Linux identisch.
- **`restic forget --group-by tags,paths` (ohne host):** Der Hostname des
  Backup-Containers ändert sich bei jedem Lauf — Gruppierung über den Host
  würde die Retention zerstören.
- **Exec-Backups:** Nur stdout fließt in den Snapshot, stderr geht in die Logs.
  Exit-Code ≠ 0 ⇒ Job fehlgeschlagen, auch wenn restic sauber beendet.
- **Quellpfad-Check vor Mount-Backups:** `os.Stat` — fehlt der pfadidentische
  ro-Mount des Volume-Roots, gibt es einen klaren Fehler statt eines leeren
  Snapshots.
- **Container ohne Backup-Label** werden gesammelt als eine INFO-Zeile geloggt;
  Container mit `stack-backup.*`-Labels ohne `enable=true` einzeln als WARN.
- **Cleanup per `defer`:** Container-Neustart (`stop=true`) und `post-command`
  laufen auch bei Fehlern/Panic während des Backups.

## Umfeld & Stolpersteine

- Produktion: natives Linux, Volumes fast immer **Bind-Mounts auf NFS-Pfade**
  (`/mnt/docker-nfs-01/volumes/<stack>/<service>/…`); benannte Volumes selten,
  aber unterstützt. S3-Target ist Synology C2 (der minio im Stack ist
  Backup-Objekt, nie Ziel).
- Entwicklung: Docker Desktop unter WSL. Bind-Mounts funktionieren dort;
  benannte Volumes liegen in der VM und sind über einen
  `/var/lib/docker/volumes`-Mount **nicht** erreichbar.
- `docker run -v` braucht absolute Host-Pfade (anders als Compose) — betrifft
  Test-Skripte und README-Beispiele.
- restic behält den ältesten Tages-Snapshot, solange weniger Tage Historie
  existieren als `keep_daily` — sieht nach einem Retention-Bug aus, ist keiner.
