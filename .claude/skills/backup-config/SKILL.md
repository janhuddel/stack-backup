---
name: backup-config
description: Schlägt stack-backup-Labels für einen Docker-Service vor — Mount-Backup vs. Exec-Dump, stop/Hooks, Excludes, Targets. Nutzen, wenn gefragt wird "wie sichere ich <Service>", "Backup-Config/Labels für <Service>", oder ein compose-Snippet zur Bewertung kommt.
---

# Backup-Config für einen Service vorschlagen

Ziel: aus den Fakten eines Dienstes einen **begründeten** Label-Vorschlag ableiten.
Ergebnis ist ein Vorschlag im Chat — dieser Skill ändert keine Dateien. Labels in eine
compose-Datei schreiben nur, wenn der Nutzer das ausdrücklich verlangt.

Antworten auf deutsch (siehe [AGENTS.md](../../../AGENTS.md)).
Label-Referenz nicht duplizieren, sondern nachschlagen: [README.md](../../../README.md),
Abschnitt `## Label-Referenz`.

## 1. Fakten sammeln

Der produktive Stack läuft auf dem Server und ist von hier aus **nicht** erreichbar —
also nichts live inspizieren, sondern die Fakten vom Nutzer holen. Gebraucht werden:

| Fakt | Wofür |
|---|---|
| Image + Tag | Klassifizierung, verfügbare CLI-Tools im Image |
| Containername (`<stack>.<service>`) | Snapshot-Tag, `exec.filename`-Default |
| **Alle** Mounts als Quellpfad → Containerpfad | `volumes`-Selektoren, `all`-Entscheidung, Quellpfad-Check |
| Externe Datenbank im Spiel? | Klasse H — Dump gehört dann nicht hierher |
| Downtime vertretbar? Wer nutzt den Dienst? | `stop` vs. Hooks vs. heiß |
| Grobes Datenvolumen | Target-Wahl, Exec-Kosten pro Target |

Fehlt etwas: **gezielt nachfragen, nie raten.** Erfundene Mount-Pfade sind der häufigste
Weg zu einem Vorschlag, der bei jedem Lauf scheitert. Passender Einzeiler für den Nutzer:

```sh
docker inspect -f '{{json .Mounts}}' <container> | jq
```

Alternativ reicht der compose-Service-Block. Bei unklarer Persistenz zusätzlich:

```sh
docker exec <container> sh -c 'ls -la <datenpfad>'
```

## 2. Klassifizieren

Genau **eine** Klasse bestimmen — Entscheidungsbaum und Rezepte in
[references/strategien.md](references/strategien.md):

A Stateless · B Reine Dateien · C Embedded-DB · D DB-Server im Container ·
E Eigenes Backup-Tool · F Quiesce per Hook · G Objektspeicher · H Extern gesichert

Mischfälle gibt es (App mit lokalen Dateien *und* externer DB → B + H). Dann beide
Klassen benennen und begründen, warum der eine Teil hier gesichert wird und der andere nicht.

## 3. Vorschlag bauen

Labels in compose-**Listenform** (`- key=value`), so wie die Stacks es schreiben:

```yaml
    labels:
      - stack-backup.enable=true
      - stack-backup.volumes=/app/data
      - stack-backup.stop=true
      - stack-backup.volume./app/data.exclude=cache/**,*.tmp
      - 'stack-backup.exec.command=sh /opt/backup-hooks/dump.sh'
```

Zwei Regeln dazu:

- **Werte nie einzeln quoten.** Compose trennt nur am ersten `=` und übernimmt den Rest
  wörtlich — aus `stack-backup.stop="true"` wird der Wert `"true"` **mit** Anführungs­zeichen,
  und der Vergleich auf `true` schlägt fehl. Der Container wird dann stillschweigend nicht
  gestoppt.
- Enthält ein Wert Leerzeichen oder Sonderzeichen (Exec-Kommandos), den **ganzen Eintrag**
  in einfache Anführungszeichen setzen — `- 'stack-backup.exec.command=…'`.

`$` bleibt auch in Listenform verdoppelt (`$$rc`), Interpolation hängt nicht an der Schreibweise.

## 4. Prüfen

Vor der Ausgabe die Prüfliste in [references/pruefung.md](references/pruefung.md)
durchgehen — nicht danach.

## 5. Ausgeben

Festes Format:

1. **Labels** als compose-Block (copy-paste-fähig).
2. **Begründung** — 2–4 Sätze pro Entscheidung: warum diese Klasse, warum stop/kein stop,
   warum diese Excludes, warum diese Targets.
3. **Restrisiken** — was der Vorschlag *nicht* abdeckt (z.B. „xl.meta genau während des
   Reads aktualisiert", „Dump ist logisch, keine Point-in-Time-Recovery").
4. **Restore** — ein konkreter Einzeiler für genau diesen Vorschlag (Dump per `dump …|`
   zurückspielen bzw. Mount per `restore --tag …`; Vorlagen im README unter `## Restore`).
5. **Verifikation** — Kommandos aus [references/pruefung.md](references/pruefung.md),
   mit denen der Nutzer den ersten Lauf auf dem Server kontrolliert.

## Harte Regeln

- **Keine Secrets in Labels.** Labels landen im Git. Tokens/Passwörter kommen über
  `env_file` oder `environment` mit `${VAR}` in den Container, das Exec-Kommando liest sie
  als Env-Variable.
- **`volumes: all` nur, wenn jeder Mount des Containers ein Datenverzeichnis unter einem
  read-only gemounteten Volume-Root ist.** Sobald eine einzelne Config-Datei aus dem
  Compose-Verzeichnis dabei ist (`./config/app.yml:/etc/app/app.yml`), scheitert der
  Quellpfad-Check bei *jedem* Lauf → Datenpfade explizit auflisten.
- **Exec-Kommandos laufen einmal pro Target.** Bei teuren Dumps (großes Tar, langer
  `pg_dump`) `stack-backup.targets` einschränken und das im Vorschlag begründen.
- **Nur stdout landet im Snapshot.** Fortschritts-/Statusausgabe von Tools mit `1>&2`
  umleiten, Exit-Code über `; rc=$?; …; exit $rc` erhalten — sonst gilt ein
  fehlgeschlagener Dump als erfolgreiches Backup.
- **`$` in compose-Labels verdoppeln.** Compose interpoliert `$VAR` und `${VAR}` beim
  Einlesen der Datei — aus `exit $rc` wird sonst `exit `, aus `$MYSQL_PWD` ein leerer
  String. In der compose-Datei also `$$rc`, `$$MARIADB_ROOT_PASSWORD`, `$$(ls -t …)`
  schreiben; im Container kommt daraus ein einfaches `$`. Wer die Labels per
  `docker run --label` setzt, schreibt ein einfaches `$`. Der ausgegebene Block ist
  compose — also verdoppeln und den Grund dazusagen.
- **`enable: "true"` allein tut nichts.** Ohne `exec.command` oder `volumes` gibt es nur
  eine Warnung im Log.
- **Excludes sind relativ zum Mount-Root** und dürfen nie das WAL/Journal einer Datenbank
  treffen, die kalt mitgesichert wird.
