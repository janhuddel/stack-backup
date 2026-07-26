package runner

import (
	"bytes"
	"log/slog"
	"strings"
)

// logWriter leitet einen Byte-Strom zeilenweise an slog weiter — für stderr
// von Container-Kommandos, damit deren Ausgabe in den Backup-Logs landet.
type logWriter struct {
	log *slog.Logger
	buf bytes.Buffer
}

func newLogWriter(log *slog.Logger) *logWriter {
	return &logWriter{log: log}
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// Unvollständige Zeile zurücklegen und auf mehr Daten warten.
			w.buf.WriteString(line)
			break
		}
		w.logLine(line)
	}
	return len(p), nil
}

// Flush loggt eine eventuell verbliebene Zeile ohne abschließendes \n.
func (w *logWriter) Flush() {
	if w.buf.Len() > 0 {
		w.logLine(w.buf.String())
		w.buf.Reset()
	}
}

func (w *logWriter) logLine(line string) {
	if line = strings.TrimRight(line, "\r\n"); line != "" {
		w.log.Info(line)
	}
}
