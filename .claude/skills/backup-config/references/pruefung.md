# Prüfung — vor der Ausgabe und nach dem ersten Lauf

## 1. Prüfliste vor der Ausgabe

Jeden Punkt beantworten; ein „nein" heißt Vorschlag korrigieren, nicht relativieren.

**Mounts**
- [ ] Liegt der Quellpfad **jedes** vorgeschlagenen Mounts unter einem Volume-Root, den der
      Backup-Container read-only und pfadidentisch gemountet hat (hier typisch
      `/mnt/docker-nfs-01/volumes/…`, bei benannten Volumes `/var/lib/docker/volumes`)?
- [ ] Bei `volumes: all` — ist wirklich **jeder** Mount des Containers ein Datenverzeichnis?
      Eine einzelne Config-Datei aus dem Compose-Verzeichnis genügt, um jeden Lauf scheitern
      zu lassen. Im Zweifel Pfade explizit auflisten.
- [ ] Sind die Selektoren die **Container**-Pfade (bzw. Volume-Namen), nicht die Host-Pfade?
- [ ] Ist bei einem `volume.<pfad>.exclude`-Label der Pfad exakt der Container-Pfad des
      Mounts, inklusive führendem Slash (`stack-backup.volume./app/data.exclude`)?

**Excludes**
- [ ] Relativ zum Mount-Root formuliert, nicht absolut?
- [ ] Trifft kein Pattern ein DB-Journal, WAL oder Schlüsselmaterial? (Liste in
      [strategien.md](strategien.md), Abschnitt Exclude-Kochbuch.)
- [ ] Hat jedes Exclude eine Begründung im Vorschlagstext?

**Exec**
- [ ] Existiert das Kommando im Image? (`pg_dump` fehlt in App-Images, `mariadb-dump` heißt
      in älteren Images `mysqldump`, `rcon-cli` gibt es nur in den itzg-Images.)
- [ ] Mehr als ~drei verkettete Anweisungen? Dann Wrapper-Skript + `exec.script` statt
      `exec.command` vorschlagen (Harte Regeln in [SKILL.md](../SKILL.md)).
- [ ] Bei `exec.script`: steht der zugehörige ro-Bind-Mount des Skripts mit im Vorschlag,
      ist der Labelwert der **Container**-Pfad, und ist er ein reiner Pfad ohne
      Leerzeichen/Shell-Zeichen? (Sonst warnt das Tool und der Aufruf `sh <pfad>`
      zerfällt.) Achtung bei `volumes=all` am selben Container: der Skript-Mount wird
      dann zum Mount-Job und scheitert am Quellpfad-Check.
- [ ] Fortschritts-/Statusausgabe mit `1>&2` umgeleitet, damit nur Nutzdaten in den Snapshot
      fließen?
- [ ] Exit-Code erhalten (`; rc=$?; …; exit $rc`), wenn nach dem Dump noch aufgeräumt wird?
- [ ] Jedes `$` im compose-Label verdoppelt (`$$rc`, `$$VAR`, `$$(…)`)? Compose interpoliert
      sonst beim Einlesen und schickt ein leeres Wort in den Container.
- [ ] `exec.filename` gesetzt und sprechend? Ohne Label heißt die Datei
      `<containername>.dump`.
- [ ] Ist der Dump teuer und laufen mehrere Targets? Dann `stack-backup.targets`
      einschränken — das Kommando läuft je Target erneut.

**Allgemein**
- [ ] Keine Passwörter, Tokens oder Schlüssel im Label (Labels landen im Git)?
- [ ] Ergibt der Vorschlag mindestens einen Job — `exec.command`, `exec.script` **oder**
      `volumes`? `enable=true` allein erzeugt nur eine Warnung.
- [ ] Labels in Listenform (`- key=value`) und **kein** Wert einzeln gequotet? `stop="true"`
      liefert den Wert `"true"` inklusive Anführungszeichen — der Vergleich auf `true`
      schlägt fehl, der Container wird stillschweigend nicht gestoppt. Werte mit
      Leerzeichen: ganzen Eintrag in einfache Anführungszeichen.
- [ ] Ist der Restore-Weg für genau diesen Vorschlag beschrieben?
- [ ] Bei Klasse H: steht ausdrücklich da, welcher Teil der Daten **woanders** gesichert wird?

## 2. Verifikation auf dem Server

Nach dem Setzen der Labels — Kommandos so an den Nutzer geben:

```sh
# 1. Container mit neuen Labels neu erzeugen (Labels sind Erzeugungszeit-Metadaten!)
docker compose up -d <service>

# 2. Labels so prüfen, wie sie im Container ankommen — nicht per "compose config",
#    das escapte "$$" wieder als "$$" ausgibt und den Fehler verdeckt
docker inspect -f '{{json .Config.Labels}}' <container> | jq 'with_entries(select(.key|startswith("stack-backup")))'

# 3. Backup-Lauf sofort antriggern und Log verfolgen
docker kill --signal=USR1 <backup-container>
docker logs -f <backup-container>

# 4. Snapshot vorhanden?
docker compose run --rm backup restic --target local -- \
  snapshots --tag container=<containername>

# 5. Inhalt stichprobenartig prüfen
docker compose run --rm backup restic --target local -- \
  ls latest --tag container=<containername>,mount=/data

# 6. Restore-Probe in ein Testverzeichnis
docker compose run --rm backup restic --target local -- \
  restore latest --tag container=<containername> --target /tmp/restore-test
```

Schritt 1 ist der meistübersehene: geänderte Labels wirken erst, wenn der Container neu
erzeugt wurde — ein `restart` genügt nicht.

Warnt `docker compose up` mit `The "rc" variable is not set. Defaulting to a blank string.`,
ist ein `$` im Label nicht verdoppelt — das Kommando kommt verstümmelt im Container an.

## 3. Fehlermeldungen im Log → Ursache

| Meldung | Ursache | Fix |
|---|---|---|
| `mount <pfad>: Quellpfad <host-pfad> nicht im Backup-Container verfügbar (Volume-Root mounten!)` | Der Host-Quellpfad des Mounts ist im Backup-Container nicht sichtbar — meist eine gemountete Config-Datei aus dem Compose-Verzeichnis bei `volumes: all`, oder ein Volume-Root, der dem Backup-Service fehlt | Datenpfade explizit auflisten statt `all`, oder den Volume-Root read-only und pfadidentisch in den Backup-Service mounten |
| `container <name>: enable=true, aber weder exec.command/exec.script noch volumes gesetzt` | Opt-in gesetzt, aber kein Job definiert | `volumes`, `exec.command` oder `exec.script` ergänzen |
| `container <name>: exec.command und exec.script gesetzt — exec.script gewinnt` | Beide Exec-Labels gesetzt | eines der beiden entfernen (gemeint ist fast immer `exec.script`) |
| `container <name>: exec.script "…" enthält Shell-Sonderzeichen …` | Wert ist keine reine Pfadangabe — Kommandozeile im falschen Label | Kommandozeile nach `exec.command` verschieben oder ins Skript verlagern |
| `sh: … <pfad>: not found` bzw. `cannot open <pfad>` im exec-stderr | Skript-Mount fehlt oder Pfad falsch | ro-Bind-Mount des Skripts prüfen (`docker exec <container> ls -l <pfad>`) |
| `container <name>: kein Mount passt zu "<sel>"` | Selektor trifft keinen Mount — Tippfehler, Host- statt Container-Pfad, oder der Mount existiert nicht mehr | Selektor gegen `docker inspect -f '{{json .Mounts}}'` prüfen |
| `container <name>: unbekanntes Target "<name>" ignoriert` | Target-Name steht nicht in der Config | Namen gegen `targets:` in der `config.yml` abgleichen |
| `container <name>: kein gültiges Target übrig, Container übersprungen` | Alle Namen im `targets`-Label waren unbekannt | wie oben |
| `container <name>: volumes=all, aber keine Mounts gefunden` | Container hat keine Bind-Mounts/benannten Volumes (andere Mount-Typen werden gefiltert) | Klasse prüfen — vermutlich stateless oder Exec-Fall |
| WARN über einen Container mit `stack-backup.*`-Labels, aber ohne Lauf | `enable=true` fehlt oder ist nicht exakt `"true"` | Label ergänzen/korrigieren |
| Exec-Job schlägt fehl, restic meldet aber Erfolg | Exit-Code ≠ 0 des Kommandos — der Job gilt korrekt als fehlgeschlagen | stderr im Log lesen; oft fehlendes Tool oder fehlende Credentials im Container-Env |
| Exec-Kommando bricht mit Syntaxfehler ab oder authentifiziert sich mit leerem Passwort | Nicht verdoppeltes `$` im compose-Label — Compose hat `$rc`/`$VAR` beim Einlesen ersetzt | `$$` schreiben und mit `docker inspect … .Config.Labels` gegenprüfen |

## 4. Was der erste erfolgreiche Lauf noch nicht beweist

Im Vorschlag benennen, wenn relevant:

- Ein Snapshot bedeutet nicht, dass die Daten **konsistent** sind — das entscheidet die
  Klasse (Stop/Hook/Dump), nicht der Exit-Code.
- Restore ungetestet ist Backup ungetestet. Die Restore-Probe aus Schritt 6 gehört zur
  Inbetriebnahme, nicht auf die Liste für später.
- restic behält den ältesten Tages-Snapshot, solange weniger Tage Historie existieren als
  `keep_daily` — das sieht nach einem Retention-Bug aus und ist keiner.
