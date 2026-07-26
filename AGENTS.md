# stack-backup — Hinweise für Coding-Agents

Label-gesteuertes Backup-Tool für Docker-Stacks (Go + restic als CLI-Subprozess).
Läuft als Container neben dem Stack, liest per Cron alle laufenden Container über
den Docker-Socket und sichert, was per `stack-backup.*`-Label konfiguriert ist.
Fachliche Details und Label-Referenz: [README.md](README.md).

## Skills

- `.claude/skills/backup-config` — Verfahren für Label-Vorschläge zu einem Dienst
  (Entscheidungsbaum, Rezept-Katalog nach Persistenz-Klasse, Prüfliste). Neue Rezepte
  gehören dorthin; die README-Tabelle bleibt die Kurzfassung.

## Sprache & Stil

- Kommentare, Doku und Commit-Messages sind **deutsch** — dabei bleiben.
- **Log- und Fehlermeldungen dagegen englisch**: klein beginnend, kurze Sätze mit
  Leerzeichen (dann quotet slog die msg einheitlich), laufende Aktionen im
  Verlaufsform-Stil (`stopping container …`), Ereignisse deklarativ
  (`backup iteration finished`). Kein Gedankenstrich in Meldungen, Kommata nutzen.
- Bezeichner dagegen **englisch**: Variablen, Funktionen, Felder, Label-Namen. Gilt auch
  für Shell-Skripte in Vorschlägen (`archive=…`, nicht `archiv=…`).
- Logging über `log/slog` (strukturiert, stdout). Fehler werden pro Job gesammelt
  (`errors.Join`), ein fehlgeschlagener Job bricht den Lauf nie ab.

## Build & Test

```sh
go build -o bin/stack-backup ./cmd/stack-backup   # Binary nach bin/ (gitignored)
go vet ./...
go test ./...                 # Unit-Tests (plan, config, docker/classify)
docker build -t stack-backup .   # Multi-Stage: golang:1.25-alpine → alpine + restic
```

Manuell gegen einen echten Stack: Image bauen, dann mit `--once` und einem
lokalen `local`-Target (Filesystem-Repository) laufen lassen — Aufrufbeispiele
siehe [README.md](README.md) (`docker run` braucht absolute Host-Pfade).

## Architektur (Paket → Verantwortung)

- `cmd/stack-backup` — Flags (`--config`, `--once`), Cron-Daemon mit
  Signal-Handling (SIGUSR1 = sofortige Iteration), Subcommand
  `restic --target NAME -- ARGS` (Passthrough mit fertigem Target-Env für
  Inspektion/Restore).
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
- **Selbstausschluss (best effort):** Der Backup-Container filtert sich selbst aus der
  Discovery, damit er nicht im Skipped-Log auftaucht und sich nie selbst sichert oder
  stoppt. Die eigene ID kommt aus `/proc/self/mountinfo`, ersatzweise
  `/proc/self/cgroup`, zuletzt aus dem Hostname (= Kurz-ID, sofern nicht überschrieben).
  Verglichen wird nur gegen Container-IDs, nie gegen Namen: ein gesetzter `hostname:`
  deaktiviert höchstens den Ausschluss, er trifft nie den falschen Container.
- **Health-Gate (`require_healthy`, Default true):** Container mit Healthcheck werden
  nur gesichert, wenn sie beim Start des Backup-Versuchs `healthy` melden — sonst
  WARN und übersprungen (zählt weder als ok noch als failed). Kein Healthcheck
  definiert ⇒ gilt als OK. Der Status wird frisch per Inspect geprüft, aber immer
  **vor** dem eigenen Stop — nie danach re-checken, ein von uns gestoppter Container
  ist absichtlich nicht healthy.
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
  die README-Beispiele.
- restic behält den ältesten Tages-Snapshot, solange weniger Tage Historie
  existieren als `keep_daily` — sieht nach einem Retention-Bug aus, ist keiner.
