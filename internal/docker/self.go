package docker

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strings"
)

// Die eigene Container-ID steckt in /proc/self/mountinfo: Docker bindet
// resolv.conf, hostname und hosts aus /var/lib/docker/containers/<id>/ ein.
var mountinfoIDRe = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// Unter cgroup v1 endet der Pfad auf die Container-ID, je nach Treiber als
// ".../docker/<id>" oder ".../docker-<id>.scope".
var cgroupIDRe = regexp.MustCompile(`(?:^|[/-])([0-9a-f]{64})(?:\.scope)?$`)

// selfContainerID ermittelt die ID des eigenen Containers (best effort).
// Liefert "", wenn sie sich nicht bestimmen lässt — dann entfällt der
// Selbstausschluss, ohne dass etwas fehlschlägt.
func selfContainerID() string {
	if id := idFromFile("/proc/self/mountinfo", parseMountinfoID); id != "" {
		return id
	}
	if id := idFromFile("/proc/self/cgroup", parseCgroupID); id != "" {
		return id
	}
	// Fallback: Docker setzt den Hostname per Default auf die 12-stellige
	// Kurz-ID. Ein selbst gesetzter hostname trifft auf keine ID zu und
	// deaktiviert damit nur den Ausschluss.
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

func idFromFile(path string, parse func(io.Reader) string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	return parse(f)
}

// parseMountinfoID sucht die Container-ID in einem /proc/self/mountinfo.
func parseMountinfoID(r io.Reader) string {
	return scanLines(r, func(line string) string {
		if m := mountinfoIDRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
		return ""
	})
}

// parseCgroupID sucht die Container-ID in einem /proc/self/cgroup. Unter
// cgroup v2 steht dort oft nur "0::/" — dann bleibt das Ergebnis leer.
func parseCgroupID(r io.Reader) string {
	return scanLines(r, func(line string) string {
		_, path, ok := strings.Cut(line, ":")
		if !ok {
			return ""
		}
		if _, path, ok = strings.Cut(path, ":"); !ok {
			return ""
		}
		if m := cgroupIDRe.FindStringSubmatch(path); m != nil {
			return m[1]
		}
		return ""
	})
}

func scanLines(r io.Reader, match func(string) string) string {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if id := match(scanner.Text()); id != "" {
			return id
		}
	}
	return ""
}

// isSelf meldet, ob containerID der eigene Container ist. Verglichen wird nur
// gegen die ID (nie gegen Namen) und erst ab 12 Zeichen: ein überschriebener,
// kurzer Hostname darf niemals zufällig auf einen fremden Container passen.
func isSelf(containerID, self string) bool {
	if len(self) < 12 {
		return false
	}
	return strings.HasPrefix(containerID, self)
}
