# stack-backup

Label-gesteuertes Backup-Tool für Docker-Stacks auf Basis von [restic](https://restic.net/).
Läuft als Container neben dem Stack, liest bei jedem Cron-Lauf alle laufenden
Container über den Docker-Socket ein und sichert, was per Label konfiguriert ist —
in beliebig viele restic-Repositories mit jeweils eigener Retention.

## Funktionsweise

Bei jedem Lauf (globaler Cron-Ausdruck in der Config):

1. Alle laufenden Container mit `stack-backup.enable=true` werden ermittelt. Der
   Backup-Container selbst wird dabei erkannt und übersprungen — er sichert sich
   nie selbst und taucht auch nicht in den Log-Meldungen über ignorierte
   Container auf.
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

Weitere globale Optionen (alle optional):

- `label_prefix` — Prefix der Container-/Volume-Labels (Default: `stack-backup`).
- `stop_timeout_seconds` — Timeout für `docker stop` bei `stop=true` (Default: `30`).
- `require_healthy` — Container mit Healthcheck werden nur gesichert, wenn sie
  beim Start des Backup-Versuchs `healthy` melden; bei `starting`/`unhealthy`
  wird der Container mit Warnung übersprungen (zählt nicht als Fehler).
  Container ohne definierten Healthcheck werden immer gesichert.
  `false` schaltet die Prüfung ab. (Default: `true`)

## Label-Referenz

### Container-Labels

| Label | Bedeutung |
|---|---|
| `stack-backup.enable=true` | Opt-in — ohne dieses Label passiert nichts. Ignorierte Container werden pro Lauf gesammelt geloggt; trägt ein Container zwar `stack-backup.*`-Labels, aber kein `enable=true`, gibt es eine Warnung (vermutlich vergessen) |
| `stack-backup.exec.command=pg_dump -U postgres mydb` | Kommando im laufenden Container; stdout → Snapshot |
| `stack-backup.exec.script=/opt/backup-hooks/dump.sh` | Alternative zu `exec.command`: Pfad eines Skripts **im Container** (typisch per Bind-Mount aus dem Stack-Repo), gestartet via `sh <pfad>` — kein Quoting im Label, kein Execute-Bit nötig. Sind beide gesetzt, gewinnt `exec.script` (mit Warnung) |
| `stack-backup.exec.filename=mydb.sql` | Dateiname im Snapshot (Default: `<containername>.dump`) |
| `stack-backup.volumes=all` bzw. `/data,/config` | Welche Mounts gesichert werden. Identifikation über den Container-Pfad; bei benannten Volumes auch der Volume-Name |
| `stack-backup.volume./data.exclude=cache/**,tmp/**` | Excludes pro Mount (Patterns relativ zum Mount-Root) |
| `stack-backup.stop=true` | Container während der Mount-Backups stoppen (SQLite & Co.), Neustart garantiert |
| `stack-backup.pre-command=…` / `stack-backup.post-command=…` | Hooks vor/nach den Mount-Backups per `docker exec` — Konsistenz ohne Stop |
| `stack-backup.targets=local,c2` | Nur in bestimmte Targets sichern (Default: alle) |

#### Vertrag für Exec-Kommandos und -Skripte

Gilt für `exec.command` und `exec.script` gleichermaßen:

- **stdout ist der Snapshot-Inhalt** — jedes Byte auf stdout landet in der
  Backup-Datei (`exec.filename`). Fortschritts-, Status- und Fehlermeldungen
  deshalb nach **stderr** umleiten (`>&2`); stderr erscheint zeilenweise im
  Backup-Log. Tools, die eine Datei erzeugen statt nach stdout zu schreiben:
  eigene Ausgabe nach stderr, dann die Datei `cat`en (Beispiele unten).
- **Exit-Code ≠ 0 ⇒ Job fehlgeschlagen** — auch wenn restic den (Teil-)Stream
  sauber gespeichert hat. Umgekehrt gilt Exit-Code 0 als Erfolg, selbst wenn
  stdout leer war: Skripte mit `set -eu` schreiben (bzw. Aufräumarbeiten mit
  `rc=$?; …; exit $rc` absichern), damit ein gescheiterter Dump nie als
  erfolgreiches Backup durchgeht.
- Ausführung via `sh -c` (Pipes und Verkettungen funktionieren) als der im
  Image definierte **USER**, mit dem vollen **Env des Containers** (env_file-
  Variablen sind verfügbar). Kein TTY, **stdin ist leer** — interaktive
  Rückfragen lesen EOF.
- Das Kommando läuft **einmal pro Target** — teure Dumps ggf. per
  `stack-backup.targets` einschränken.

Ab etwa drei verketteten Anweisungen wird das Label unleserlich und das
`$$`-Escaping fehleranfällig — dann das Kommando als Skript ins Stack-Repo
legen, read-only in den Container mounten und per `exec.script` referenzieren
(Beispiel unten bei ioBroker).

**Vorsicht mit `volumes=all` bei gemounteten Config-Dateien.** `all` nimmt
*jeden* Bind-Mount und jedes benannte Volume des Containers — auch einzelne
Dateien aus dem Compose-Verzeichnis, wie sie oft für Konfiguration gemountet
werden (`./config/app/config.yml:/etc/app/config.yml`). Deren Quellpfad liegt
nicht unter den read-only gemounteten Volume-Roots, der Quellpfad-Check schlägt
fehl und der Container wird bei jedem Lauf als fehlgeschlagen gezählt. In dem
Fall die Datenpfade explizit auflisten (`volumes=/var/lib/app`) — Config aus dem
Compose-Repo gehört ohnehin ins Git, nicht ins restic-Repository.

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
| ioBroker | `exec.script=/opt/backup-hooks/iobroker-backup.sh` + `exec.filename=iobroker-backup.tar.gz` (Skript siehe unten) |
| InfluxDB 2.x | `volumes=/var/lib/influxdb2` + `stop=true` (siehe unten) |
| MinIO (SNSD) | `volumes=all` + `volume./data.exclude=.minio.sys/tmp/**,.minio.sys/multipart/**` |
| Vaultwarden (DB extern) | `volumes=all` (Attachments/Keys; DB wird auf dem DB-Server gesichert) |
| Stateless (traefik, dozzle, …) | kein Label |

Hinweis: Bei mehreren Targets wird ein Exec-Kommando pro Target erneut
ausgeführt (bewusster Trade-off: kein Zwischenspeichern auf Platte).

### ioBroker (Muster: Wrapper-Skript per `exec.script`)

`iobroker backup` erzeugt einen konsistenten Export ohne Downtime, schreibt ihn
aber als Datei und meldet den Pfad auf stdout — genau der Fall, in dem ein
Wrapper-Skript besser lesbar ist als ein verkettetes Label. Das Skript liegt im
Stack-Repo und wird read-only in den Container gemountet:

```yaml
    volumes:
      - ./scripts/iobroker-backup.sh:/opt/backup-hooks/iobroker-backup.sh:ro
    labels:
      - stack-backup.enable=true
      - stack-backup.exec.script=/opt/backup-hooks/iobroker-backup.sh
      - stack-backup.exec.filename=iobroker-backup.tar.gz
```

`scripts/iobroker-backup.sh`:

```sh
#!/bin/sh
# Backup-Kommando für stack-backup (Label: stack-backup.exec.script).
# Nur stdout landet im Snapshot; Exit-Code != 0 lässt den Job fehlschlagen.
set -eu

# CLI-Ausgabe einsammeln und geschlossen ins Backup-Log (stderr) schieben.
output=$(iobroker backup)
echo "$output" >&2

# Archivpfad aus "Backup created: …" ziehen; ohne Treffer endet grep mit 1.
archive=$(echo "$output" | grep -o "/opt/iobroker/backups/.*\.tar\.gz")

# Archiv nach dem Streamen wegräumen — die Historie hält restic.
trap 'rm -f "$archive"' EXIT

cat "$archive"
```

Skript-Änderungen wirken sofort beim nächsten Lauf; nur Label-Änderungen
brauchen eine Neuerzeugung des Containers (`docker compose up -d`).

### InfluxDB 2.x

`/var/lib/influxdb2` enthält `influxd.bolt` (Buckets, Orgs, Tokens),
`influxd.sqlite` (Tasks, Notebooks, Dashboards) und `engine/` mit TSM-Dateien
plus WAL. Bolt und SQLite sind echte Datenbank-Dateien — ein Kopieren im
laufenden Betrieb ist nicht sicher, beim geordneten Stop schließt influxd beide
sauber. Das Verzeichnis ist danach vollständig restore-fähig.

Gegen `influx backup` spricht dreierlei: das Tool schreibt erst komplett in ein
Temp-Verzeichnis im Container (verdoppelt kurzzeitig den Platzbedarf auf der
Host-Disk), es braucht einen Operator-Token im Container-Env, und es erzeugt bei
jedem Lauf ein frisches Tar. TSM-Dateien sind dagegen nach dem Schreiben
unveränderlich — beim Mount-Backup überspringt restic sie, sobald es einen
Parent-Snapshot findet, und liest nur die neuen Shards ein. Das setzt einen
stabilen Hostnamen des Backup-Containers voraus (`restic backup` sucht den
Parent per Default über `host,paths`); ohne den wird bei jedem Lauf alles neu
eingelesen — dedupliziert zwar, aber langsam.

`engine/wal/**` **nicht** excluden: dort stehen die noch nicht nach TSM
kompaktierten Schreibvorgänge, die beim Start replayt werden.

Wenn die Downtime nicht vertretbar ist, doch per Exec — `INFLUX_TOKEN` über
`env_file` setzen, nicht als Label (landet sonst im Git):

```
stack-backup.exec.command=rm -rf /tmp/ibak && influx backup /tmp/ibak 1>&2 && tar -C /tmp/ibak -cf - . ; rc=$?; rm -rf /tmp/ibak; exit $rc
stack-backup.exec.filename=influxdb.tar
```

Das `1>&2` ist Pflicht, sonst mischt sich die Fortschrittsausgabe in den
Tar-Stream; `exit $rc` erhält den Exit-Code, damit ein fehlgeschlagener Dump
nicht als erfolgreiches Backup durchgeht.

### MinIO

Im SNSD-Modus (`server /data`) liegen Objekte als Verzeichnisse mit `xl.meta`
und Part-Dateien direkt im Mount — das Dateisystem-Backup erfasst Objekte,
Bucket-Metadaten, Policies und IAM in einem Rutsch. `.minio.sys/` gehört
zwingend dazu (`format.json`, ohne die startet MinIO nicht; dazu `config/`,
`buckets/` und die IAM-Daten). Ausgeschlossen werden nur die transienten
Bereiche: `tmp/` (Staging für laufende Schreibvorgänge und asynchrone
Löschungen) und `multipart/` (angefangene Uploads).

Ein Stop ist meist verzichtbar: MinIO schreibt nach `.minio.sys/tmp/` und
verschiebt fertig per Rename, halbfertige Objekte tauchen in den
Bucket-Verzeichnissen also nicht auf. Restrisiko ist ein `xl.meta`, das genau
während des Reads in-place aktualisiert wird — betrifft ein einzelnes Objekt.
`stop=true` schließt das aus, legt aber alle Dienste still, die MinIO als
S3-Backend nutzen.

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
