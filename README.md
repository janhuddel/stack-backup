# stack-backup

Label-gesteuertes Backup-Tool für Docker-Stacks auf Basis von [restic](https://restic.net/).
Läuft als Container neben dem Stack, liest bei jedem Cron-Lauf alle laufenden
Container über den Docker-Socket ein und sichert, was per Label konfiguriert ist —
in beliebig viele restic-Repositories mit jeweils eigener Retention.

## Funktionsweise

Bei jedem Lauf (globaler Cron-Ausdruck in der Config):

1. Alle laufenden Container mit `stack-backup.enable=true` werden ermittelt.
2. Pro Container wird zuerst ein optionales **Exec-Backup** ausgeführt
   (Kommando im laufenden Container, z.B. `pg_dump`; stdout fließt direkt in
   `restic backup --stdin` — nichts wird zwischengespeichert).
3. Danach die **Mount-Backups** (Bind-Mounts und benannte Volumes). Optional
   wird der Container dafür gestoppt (`stop=true`, z.B. SQLite) oder es laufen
   Pre-/Post-Hooks (z.B. Minecraft `save-off`/`save-on`). Neustart bzw.
   Post-Hook sind auch im Fehlerfall garantiert.
4. Zum Schluss wird pro Target `restic forget --prune` gemäß Retention
   ausgeführt (gruppiert nach Tags+Pfaden, jede Quelle behält ihre Historie).

Fehler einzelner Jobs brechen den Lauf nicht ab; am Ende wird eine
Zusammenfassung geloggt (strukturierte Logs auf stdout → `docker logs`).

## Setup

Siehe [docker-compose.example.yml](docker-compose.example.yml) und
[config.example.yml](config.example.yml). Wichtig:

- **Docker-Socket** mounten (`/var/run/docker.sock`). Achtung: Socket-Zugriff
  ist root-äquivalent — der Backup-Container gehört in vertrauenswürdige Hände.
- **Volume-Roots read-only und pfadidentisch** mounten, z.B.
  `/mnt/docker-nfs-01/volumes:/mnt/docker-nfs-01/volumes:ro` (Bind-Mounts) und
  `/var/lib/docker/volumes:/var/lib/docker/volumes:ro` (benannte Volumes).
  restic sichert die Host-Quellpfade der Container-Mounts; fehlt der Mount,
  meldet das Tool einen klaren Fehler statt einen leeren Snapshot zu erzeugen.
- Repository-Passwörter als Secrets/Dateien (`password_file`), S3-Credentials
  per Env (`${VAR}`-Expansion in der Config).

### Config

```yaml
schedule: "0 3 * * *"
targets:
  local:
    repository: /backups/local
    password_file: /run/secrets/restic_local
    retention:
      keep_daily: 30
  c2:
    repository: s3:https://eu-002.s3.synologyc2.net/mein-backup-bucket
    password_file: /run/secrets/restic_c2
    env:
      AWS_ACCESS_KEY_ID: ${C2_ACCESS_KEY}
      AWS_SECRET_ACCESS_KEY: ${C2_SECRET_KEY}
    retention:
      keep_last: 1
```

Retention-Felder: `keep_last`, `keep_hourly`, `keep_daily`, `keep_weekly`,
`keep_monthly`, `keep_yearly` — Mapping 1:1 auf `restic forget`.

## Label-Referenz

### Container-Labels

| Label | Bedeutung |
|---|---|
| `stack-backup.enable=true` | Opt-in — ohne dieses Label passiert nichts. Ignorierte Container werden pro Lauf gesammelt geloggt; trägt ein Container zwar `stack-backup.*`-Labels, aber kein `enable=true`, gibt es eine Warnung (vermutlich vergessen) |
| `stack-backup.exec.command=pg_dump -U postgres mydb` | Kommando im laufenden Container; stdout → Snapshot |
| `stack-backup.exec.filename=mydb.sql` | Dateiname im Snapshot (Default: `<containername>.dump`) |
| `stack-backup.volumes=all` bzw. `/data,/config` | Welche Mounts gesichert werden. Identifikation über den Container-Pfad; bei benannten Volumes auch der Volume-Name |
| `stack-backup.volume./data.exclude=cache/**,tmp/**` | Excludes pro Mount (Patterns relativ zum Mount-Root) |
| `stack-backup.stop=true` | Container während der Mount-Backups stoppen (SQLite & Co.), Neustart garantiert |
| `stack-backup.pre-command=…` / `stack-backup.post-command=…` | Hooks vor/nach den Mount-Backups per `docker exec` — Konsistenz ohne Stop |
| `stack-backup.targets=local,c2` | Nur in bestimmte Targets sichern (Default: alle) |

Alle Kommandos laufen via `sh -c` — Pipes und Verkettungen funktionieren.
**Nur stdout fließt in den Snapshot**; stderr landet in den Logs. Tools, die
eine Datei erzeugen statt nach stdout zu schreiben: eigene Ausgabe nach stderr
umleiten, dann die Datei `cat`en (Beispiele unten).

### Volume-Labels (nur benannte Volumes)

| Label | Bedeutung |
|---|---|
| `stack-backup.exclude=cache/**` | Excludes (Container-Label pro Mount gewinnt) |
| `stack-backup.enable=false` | Volume überspringen, auch bei `volumes=all` |

## Rezepte

| Anwendung | Labels |
|---|---|
| SQLite-Apps (uptime-kuma, grafana, open-webui, n8n, node-red) | `volumes=all` + `stop=true` |
| Minecraft (itzg) | `volumes=all` + `pre-command=rcon-cli save-off && rcon-cli save-all` + `post-command=rcon-cli save-on` |
| PostgreSQL | `exec.command=pg_dump -U postgres mydb` + `exec.filename=mydb.sql` |
| ioBroker | `exec.command=iobroker backup 1>&2 && cat "$(ls -t /opt/iobroker/backups/*.tar.gz \| head -1)"` + `exec.filename=iobroker-backup.tar.gz` |
| InfluxDB 2.x | `exec.command=influx backup /tmp/b 1>&2 && tar -C /tmp/b -cf - .` + `exec.filename=influxdb.tar` — oder `volumes=all` + `stop=true` |
| Vaultwarden (DB extern) | `volumes=all` (Attachments/Keys; DB wird auf dem DB-Server gesichert) |
| Stateless (traefik, dozzle, …) | kein Label |

Hinweis: Bei mehreren Targets wird ein Exec-Kommando pro Target erneut
ausgeführt (bewusster Trade-off: kein Zwischenspeichern auf Platte).

## Betrieb

```sh
# Daemon (Standard): wartet auf den Cron
docker compose up -d backup

# Lauf im laufenden Daemon sofort antriggern (Logs in "docker logs",
# über den internen Mutex gegen Cron-Läufe serialisiert)
docker kill --signal=USR1 <backup-container>

# Alternativ: einmaliger Lauf in einem eigenen Container
docker compose run --rm backup --once

# restic mit dem Env eines Targets aufrufen (Inspektion, Restore, …)
docker compose run --rm backup restic --target local -- snapshots
```

### Snapshots ansehen

Alle restic-Kommandos laufen über den Passthrough `restic --target <name> --`,
der das Env des Targets (Repository, Passwort, S3-Credentials) fertig aufbaut:

```sh
# Alle Snapshots eines Targets
docker compose run --rm backup restic --target local -- snapshots

# Snapshots sind getaggt mit container=<name>, type=exec|volume und
# mount=<containerpfad> — damit lässt sich gezielt filtern:
docker compose run --rm backup restic --target local -- \
  snapshots --tag container=vaultwarden

# Dateien im letzten Snapshot einer Quelle auflisten
docker compose run --rm backup restic --target local -- \
  ls latest --tag container=vaultwarden,mount=/data

# Was hat sich zwischen zwei Snapshots geändert? (IDs aus "snapshots")
docker compose run --rm backup restic --target local -- diff a1b2c3d4 e5f6a7b8

# Platzverbrauch des Repositories
docker compose run --rm backup restic --target local -- stats
```

Ohne Compose (z.B. beim lokalen Testen) funktioniert dasselbe per `docker run`
mit gemounteter Config, Secret und Repository-Verzeichnis — Host-Pfade müssen
hier absolut sein:

```sh
docker run --rm \
  -v "$PWD/config.yml":/etc/stack-backup/config.yml:ro \
  -v "$PWD/secrets/restic_local":/run/secrets/restic_local:ro \
  -v "$PWD/backups":/backups/local \
  stack-backup:latest restic --target local -- snapshots
```

## Restore

### Datenbank-Dump (exec-Backup)

```sh
docker compose run --rm backup restic --target local -- dump latest /mydb.sql \
  | docker exec -i postgres psql -U postgres mydb
```

### Mount/Volume

Ziel-Container stoppen, dann in den Quellpfad zurückspielen. Die Volume-Roots
sind im Backup-Container read-only gemountet — für den Restore einmalig
read-write überschreiben:

```sh
docker stop 13-applications.vaultwarden

docker compose run --rm \
  -v /mnt/docker-nfs-01/volumes:/mnt/docker-nfs-01/volumes \
  backup restic --target local -- \
  restore latest --tag container=vaultwarden,mount=/data --target /

docker start 13-applications.vaultwarden
```

(Alternativ direkt vom Host/NFS-Server aus mit lokal installiertem restic.)

### Disaster Recovery

1. Compose-Stacks starten (Verzeichnisse/Volumes entstehen leer).
2. Mount-Restores wie oben, Container für die Dauer gestoppt.
3. Dump-Restores (psql etc.) wie oben.

**Empfehlung:** Restores regelmäßig proben, z.B. in ein Testverzeichnis:

```sh
docker compose run --rm backup restic --target local -- \
  restore latest --tag container=vaultwarden --target /tmp/restore-test
```

## Entwicklung

```sh
go test ./...
go build -o bin/stack-backup ./cmd/stack-backup
docker build -t stack-backup .
```

Hinweis Docker Desktop (WSL/macOS): Die Mount-Quellen werden bewusst per
`ContainerInspect` ermittelt, weil die List-API dort VM-interne Pfade meldet.
Bind-Mounts funktionieren damit auch lokal; benannte Volumes liegen unter
Docker Desktop in der VM und sind über einen `/var/lib/docker/volumes`-Mount
aus der Distro nicht erreichbar — auf nativem Linux (Produktion) schon.
