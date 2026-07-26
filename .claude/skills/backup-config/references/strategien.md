# Strategien — Persistenz-Klassen und Rezepte

## Entscheidungsbaum

```
Schreibt der Container überhaupt Daten, die nicht aus dem Compose-Repo
wiederherstellbar sind?
├─ nein ......................................... A Stateless (kein Label)
└─ ja
   ├─ Liegen die Daten auf einem Server mit eigenem Backup
   │  (externe Postgres, NAS-Share, S3)? ........ H Extern gesichert
   ├─ Läuft im Container ein DB-Server
   │  (postgres, mariadb, mongo, redis)? ........ D Exec-Dump
   ├─ Bringt die App ein eigenes Backup-Kommando
   │  mit, das mehr kann als ein Dateikopie? .... E Eigenes Backup-Tool
   ├─ Liegt im Datenverzeichnis eine eingebettete
   │  DB-Datei (SQLite, bolt, LevelDB, LMDB)? ... C Embedded-DB (Mount + stop)
   │  └─ Stop nicht vertretbar, aber App kennt
   │     ein Flush/Lock-Kommando? .............. F Quiesce per Hook
   ├─ Objektspeicher (MinIO & Co.)? ............. G Objektspeicher
   └─ sonst: reine Dateien ...................... B Mount ohne Stop
```

Im Zweifel gilt: **Mount-Backup ist der Default**, Exec-Dump die Ausnahme für Dienste,
deren Dateiformat sich nicht sicher kalt kopieren lässt oder deren Dump deutlich kleiner
und restore-freundlicher ist.

---

## A — Stateless

**Erkennung:** keine Mounts außer Config aus dem Compose-Repo; State lebt in anderen
Diensten. Beispiele: traefik (ohne acme.json), dozzle, watchtower, Reverse-Proxys,
Exporter, zustandslose APIs.

**Labels:** keine. Der Container taucht dann in der gesammelten INFO-Zeile über ignorierte
Container auf — das ist der gewünschte Zustand.

**Warum:** Config gehört ins Git, nicht ins restic-Repository.

**Fallstricke:** traefik mit `acme.json` (Let's-Encrypt-Zertifikate) ist **nicht**
stateless — entweder Klasse B oder bewusst verzichten, weil Zertifikate neu ausgestellt
werden können. Der Verzicht gehört in die Begründung, nicht ins Schweigen.

---

## B — Reine Dateien

**Erkennung:** Datenverzeichnis enthält Medien, Uploads, generierte Artefakte, aber keine
DB-Datei. Beispiele: Foto-/Medienbibliotheken (Dateiteil), Dokumentenablagen (Dateiteil),
Registry-Blobs, statische Inhalte.

**Labels:**
```yaml
      - stack-backup.enable=true
      - stack-backup.volumes=/data
      - stack-backup.volume./data.exclude=cache/**,tmp/**
```

**Warum:** Dateien werden ganz geschrieben und umbenannt; ein Snapshot mitten im Schreiben
erwischt höchstens eine unvollständige Einzeldatei, nicht einen inkonsistenten Datenbestand.
Kein Stop nötig.

**Fallstricke & Restore:** Wenn eine DB die Datei-Metadaten hält (paperless, immich,
nextcloud), sind Dateien und DB **zusammen** konsistent zu halten — dann nicht Klasse B
allein, sondern B + D bzw. B + H, und im Vorschlag erwähnen, dass beide Teile aus
zeitnahen Snapshots stammen müssen. Restore: `restore latest --tag container=<name>,mount=/data --target /`.

---

## C — Embedded-DB (der häufigste Fall)

**Erkennung** im Datenverzeichnis:

| Signatur | Engine |
|---|---|
| `*.db`, `*.sqlite`, `*.sqlite3`, dazu `*-wal` / `*-shm` | SQLite |
| `*.bolt`, `*.boltdb`, `influxd.bolt` | bbolt |
| `CURRENT`, `MANIFEST-*`, `LOCK`, `*.ldb`/`*.sst` | LevelDB / RocksDB |
| `data.mdb`, `lock.mdb` | LMDB |
| `*.vlog`, `*.sst`, `KEYREGISTRY` | Badger |

Typische Vertreter: uptime-kuma, grafana, n8n, node-red, open-webui, vaultwarden (mit
lokaler SQLite), homeassistant, gitea (SQLite-Variante), miniflux (nein — der nutzt Postgres, Klasse D/H).

**Labels:**
```yaml
      - stack-backup.enable=true
      - stack-backup.volumes=/app/data
      - stack-backup.stop=true
      - stack-backup.volume./app/data.exclude=cache/**,*.tmp
```

**Warum:** Eine offene SQLite-/bolt-Datei im laufenden Betrieb zu kopieren liefert im
besten Fall eine Datei, die beim Start ein Journal-Replay braucht, im schlechten Fall eine
korrupte. Beim geordneten Stop schließt der Prozess sauber, danach ist das Verzeichnis
vollständig restore-fähig. Der Stop gilt nur für die Dauer der Mount-Backups, der Neustart
ist per `defer` auch bei Fehlern garantiert.

**Fallstricke:**
- `*-wal`, `*-shm`, `*.journal` **nie** excluden. Nach einem Stop sind sie meist leer oder
  weg; existieren sie, gehören sie zum Datenbestand.
- Wenn die App eine eigene Export-Funktion hat, ist die trotzdem selten die bessere Wahl:
  sie braucht Credentials und erzeugt jedes Mal ein frisches Artefakt (schlechte
  Deduplizierung). Mount + Stop ist einfacher und inkrementell.
- Downtime ist die Dauer des Mount-Backups, nicht der Retention. Bei vielen kleinen Dateien
  eher Sekunden bis wenige Minuten.

**Restore:** Container stoppen, `restore latest --tag container=<name>,mount=/app/data --target /`,
Container starten.

---

## D — DB-Server im Container

**Erkennung:** Image ist ein Datenbankserver; andere Container verbinden sich per Netzwerk.

**Warum Dump statt Mount:** Ein Dump ist konsistent ohne Stop, deutlich kleiner als das
Datenverzeichnis und beim Restore versions- und plattformunabhängig. Der Preis: kein
Point-in-Time-Recovery, und der Dump wird bei jedem Lauf komplett neu erzeugt (restic
dedupliziert das Ergebnis, muss es aber jedes Mal einlesen).

**PostgreSQL**
```yaml
      - stack-backup.enable=true
      - 'stack-backup.exec.command=pg_dump -U postgres --clean --if-exists mydb'
      - stack-backup.exec.filename=mydb.sql
```
Mehrere Datenbanken plus Rollen: `pg_dumpall -U postgres` → `filename: cluster.sql`.
Passwort nicht ins Label — der Container hat `POSTGRES_PASSWORD` im Env, `pg_dump` nutzt
lokal Peer-/Trust-Auth über den Unix-Socket.

**MariaDB / MySQL**
```yaml
      - 'stack-backup.exec.command=mariadb-dump --single-transaction --quick -u root -p"$$MARIADB_ROOT_PASSWORD" --all-databases'
      - stack-backup.exec.filename=mariadb.sql
```
`--single-transaction` ist Pflicht (konsistenter Snapshot ohne Table-Locks, nur InnoDB).
Bei älteren Images heißt das Tool `mysqldump`. Das Passwort kommt aus dem Container-Env,
steht also nicht im Label — das `$$` ist die compose-Escape-Sequenz, im Container landet
`$MARIADB_ROOT_PASSWORD`.

**MongoDB**
```yaml
      - 'stack-backup.exec.command=mongodump --archive --quiet'
      - stack-backup.exec.filename=mongo.archive
```
`--archive` ohne Dateinamen schreibt nach stdout; `--quiet` hält den Stream sauber.

**Redis** — meistens Cache und damit gar kein Backup-Objekt. Wenn doch persistent:
```yaml
      - 'stack-backup.exec.command=redis-cli save 1>&2 && cat /data/dump.rdb'
      - stack-backup.exec.filename=dump.rdb
```
`save` blockiert kurz; `bgsave` wäre asynchron und der `cat` liefe gegen eine noch
unfertige Datei.

**Fallstricke:** Prüfen, ob das Dump-Tool im Image überhaupt existiert — ein
App-Container, der eine externe Postgres nutzt, hat meist kein `pg_dump`. Bei mehreren
Targets läuft der Dump mehrfach; bei großen DBs `stack-backup.targets` einschränken.

**Restore:** `restic … dump latest /mydb.sql | docker exec -i <container> psql -U postgres mydb`.

---

## E — Eigenes Backup-Tool

**Erkennung:** Die App bringt ein Kommando mit, das mehr leistet als eine Dateikopie
(konsistenter Export inkl. Metadaten, versionsunabhängiges Format).

**Bevorzugte Form: Wrapper-Skript per `exec.script`.** Kommandos dieser Klasse sind
praktisch immer mehrgliedrig (Export → Aufräumen → Exit-Code) — das gehört als Skript
ins Stack-Repo, nicht ins Label (siehe Harte Regeln in [SKILL.md](../SKILL.md)):
```yaml
    volumes:
      - ./scripts/<dienst>-backup.sh:/opt/backup-hooks/<dienst>-backup.sh:ro
    labels:
      - stack-backup.enable=true
      - stack-backup.exec.script=/opt/backup-hooks/<dienst>-backup.sh
      - stack-backup.exec.filename=<dienst>-backup.tar.gz
```
Drei Details im Skript, alle nicht optional: `1>&2` (bzw. `>&2`) hält die
Fortschrittsausgabe aus dem Datenstrom, das aufräumende `rm` läuft per `trap … EXIT`
auch im Fehlerfall, und `set -eu` verhindert, dass ein fehlgeschlagener Dump als
erfolgreiches Backup durchgeht. Meldet das Tool den erzeugten Dateipfad auf stdout
(ioBroker: `Backup created: …`), diesen **parsen** statt per `ls -t` das neueste
Archiv zu raten — teilt sich das Verzeichnis mit anderen Schreibern (Backitup,
manuelle Läufe), erwischt `ls -t` sonst deren Datei. Komplettes Muster-Skript:
[README.md](../../../../README.md), Abschnitt `### ioBroker`.

**Einzeiler dürfen im Label bleiben** (dann `$$` für escaptes `$`, Eintrag mit
Leerzeichen als Ganzes quoten):
```yaml
      - 'stack-backup.exec.command=<tool> export --stdout'
```

**InfluxDB 2.x** ist der Lehrfall dafür, wann man das Tool *nicht* nimmt: `influx backup`
verdoppelt kurzzeitig den Platzbedarf, braucht einen Operator-Token im Env und erzeugt
jedes Mal ein frisches Tar. TSM-Dateien sind unveränderlich, das Mount-Backup überspringt
sie per Parent-Snapshot. Empfehlung deshalb `volumes: /var/lib/influxdb2` + `stop: "true"`;
die Exec-Variante nur, wenn Downtime nicht geht. Details in [README.md](../../../../README.md)
unter `### InfluxDB 2.x`.

**Fallstricke:** Tools, die zusätzlich komprimieren, verschlechtern restics Deduplizierung —
unkomprimiertes Tar ist besser, wenn das Tool die Wahl lässt. Braucht das Tool ein Token:
`env_file`, niemals ins Label.

**Restore:** `restic … dump latest /<filename> > backup.tar`, dann der Import-Weg der App.

---

## F — Quiesce per Hook

**Erkennung:** Stop wäre nötig, ist aber teuer (lange Startzeit, aktive Nutzer), und die App
kennt ein Kommando, das Schreibvorgänge flusht und pausiert.

**Labels (Minecraft, itzg-Images):**
```yaml
      - stack-backup.enable=true
      - stack-backup.volumes=/data
      - 'stack-backup.pre-command=rcon-cli save-off && rcon-cli save-all'
      - 'stack-backup.post-command=rcon-cli save-on'
      - stack-backup.volume./data.exclude=logs/**,cache/**
```

**Warum:** `save-off` stoppt das automatische Speichern, `save-all` schreibt alles
Ausstehende auf Platte — das Weltverzeichnis ist danach für die Dauer des Backups stabil,
der Server bleibt erreichbar.

**Fallstricke:**
- Der Post-Hook muss **idempotent** sein: er läuft per `defer` auch nach Fehlern und Panics,
  unter Umständen also auf einem Zustand, in dem der Pre-Hook nie lief.
- Scheitert der Pre-Hook, werden die Mount-Backups dieses Containers übersprungen — das ist
  gewollt (lieber kein Backup als ein inkonsistentes), fällt aber im Log als Fehler auf.
- Hooks laufen nur um die **Mount**-Backups, nicht um ein Exec-Backup.
- Andere Kandidaten für dieses Muster: Dienste mit `FLUSH TABLES WITH READ LOCK`-artigen
  Kommandos oder App-eigenen Wartungsmodi. Gibt es kein solches Kommando: Klasse C mit Stop.

---

## G — Objektspeicher

**MinIO im SNSD-Modus** (`server /data`):
```yaml
      - stack-backup.enable=true
      - stack-backup.volumes=all
      - stack-backup.volume./data.exclude=.minio.sys/tmp/**,.minio.sys/multipart/**
```

**Warum:** Objekte liegen als Verzeichnisse mit `xl.meta` und Part-Dateien direkt im Mount;
das Dateisystem-Backup erfasst Objekte, Bucket-Metadaten, Policies und IAM in einem Rutsch.
`.minio.sys/` gehört zwingend dazu — ohne `format.json` startet MinIO nicht. Ausgeschlossen
werden nur die transienten Bereiche.

**Fallstricke:** Stop ist meist verzichtbar (MinIO schreibt nach `.minio.sys/tmp/` und
verschiebt per Rename), schließt aber das Restrisiko eines gerade aktualisierten `xl.meta`
aus — legt dafür alle Dienste stil, die MinIO als S3-Backend nutzen. Erasure-Coded-Setups
(mehrere Laufwerke) sind ein anderer Fall und brauchen `mc mirror` statt Dateisystem-Backup.

**Wichtig für dieses Repo:** Der lokal laufende MinIO ist **Backup-Objekt**, nie Ziel.
Backup-Ziel ist Synology C2.

---

## H — Extern gesichert

**Erkennung:** Der Container hält lokal nur einen Teil der Daten; der Rest liegt auf einem
System mit eigenem Backup — bei diesem Stack typisch die **externe Postgres auf einem
separaten Server**.

**Labels:** nur die lokalen Dateien, kein Dump:
```yaml
      - stack-backup.enable=true
      - stack-backup.volumes=/data
```

**Warum:** Doppelte Sicherung derselben DB kostet Laufzeit und Speicher und erzeugt zwei
Wahrheiten mit unterschiedlichen Zeitständen. Beispiel Vaultwarden: Attachments, `rsa_key*`
und Icons liegen im Volume, die Datenbank auf dem Postgres-Server.

**Fallstricke:** Im Vorschlag **ausdrücklich** notieren, welcher Teil woanders gesichert
wird und dass ein Restore beide Teile braucht — sonst fehlt beim Disaster Recovery die
halbe Anwendung. Ebenso prüfen, ob Schlüsselmaterial (`rsa_key.pem` o.ä.) wirklich im
gesicherten Pfad liegt: ohne den Schlüssel ist die DB wertlos.

---

## Exclude-Kochbuch

Patterns sind **relativ zum Mount-Root** und kommagetrennt in einem Label.

Fast immer raus:

| Pattern | Warum |
|---|---|
| `cache/**`, `*.cache` | Wiederherstellbar, ändert sich ständig |
| `tmp/**`, `*.tmp`, `*.partial` | Transient, oft halbfertig |
| `logs/**`, `*.log` | Wächst, gehört ins Log-Management |
| `sessions/**` | Nach Restore ohnehin ungültig |
| Thumbnails/Previews | Aus Originalen regenerierbar |
| Downloads/Mods, die das Image beim Start holt | Wiederbeschaffbar, oft groß |

Nie ausschließen:

| Pattern | Warum |
|---|---|
| `*-wal`, `*-shm`, `*.journal` | SQLite-Journale — Teil des Datenbestands |
| `engine/wal/**` (InfluxDB) | Noch nicht kompaktierte Schreibvorgänge, werden beim Start replayt |
| `pg_wal/**` (bei kaltem Postgres-Mount) | Ohne WAL ist das Datenverzeichnis nicht startfähig |
| `.minio.sys/format.json` und `.minio.sys/config/**` | MinIO startet sonst nicht |
| Schlüsseldateien (`rsa_key*`, `*.pem`) | Ohne sie sind die Daten nicht entschlüsselbar |

Faustregel: Ein Exclude braucht eine Begründung im Vorschlag. Ohne Begründung lieber
mitsichern — restic dedupliziert, unnötige Excludes kosten im Zweifel den Restore.

---

## Target-Wahl

Default ist **alle** Targets. Abweichen, wenn es einen Grund gibt:

- Große, wiederbeschaffbare Datenmengen (Medien, Mod-Packs, Registry-Blobs) → nur lokal:
  `- stack-backup.targets=local`. Spart Upload und C2-Speicher.
- Klein und kritisch (Passwort-Manager, Konfiguration, DB-Dumps) → alle Targets, damit ein
  Offsite-Stand existiert.
- Teure Exec-Dumps laufen **pro Target einmal** — bei einem 10-Minuten-Dump ist die
  Einschränkung auf ein Target oft die richtige Wahl.

## Wann Stop akzeptabel ist

- Downtime ≈ Dauer des Mount-Backups, nicht des ganzen Laufs. Inkrementell und bei
  überschaubarer Dateizahl meist Sekunden.
- Der Cron läuft nachts (`0 3 * * *` im Beispiel) — für die meisten Dienste unkritisch.
- Kritisch wird es, wenn **andere Container** vom Dienst abhängen (Datenbank, S3-Backend,
  Reverse-Proxy): dann fällt mehr aus als der eine Container. In dem Fall Klasse D/F oder
  bewusstes Restrisiko ohne Stop.
- Dienste mit langer Startzeit (JVM-Anwendungen, große Indizes) leiden nicht am Stop selbst,
  sondern am Wiederanlauf — das gehört in die Abwägung.
