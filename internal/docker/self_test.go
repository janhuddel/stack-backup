package docker

import (
	"strings"
	"testing"
)

const selfID = "3f2a1b9c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a"

func TestParseMountinfoID(t *testing.T) {
	const withID = `
1234 1200 0:123 / / rw,relatime - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/l/ABC
1240 1234 0:60 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
1250 1234 259:1 /var/lib/docker/containers/` + selfID + `/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/nvme0n1p1 rw
`
	const withoutID = `
1234 1200 0:123 / / rw,relatime - overlay overlay rw
1240 1234 0:60 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
`
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"container-mount vorhanden", withID, selfID},
		{"kein container-mount", withoutID, ""},
		{"leer", "", ""},
	}
	for _, tc := range cases {
		if got := parseMountinfoID(strings.NewReader(tc.content)); got != tc.want {
			t.Errorf("%s: parseMountinfoID = %q, erwartet %q", tc.name, got, tc.want)
		}
	}
}

func TestParseCgroupID(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"cgroup v1 docker", "11:memory:/docker/" + selfID + "\n5:cpu:/docker/" + selfID + "\n", selfID},
		{"cgroup v1 systemd-scope", "1:name=systemd:/system.slice/docker-" + selfID + ".scope\n", selfID},
		{"cgroup v2 ohne id", "0::/\n", ""},
		{"host ohne container", "11:memory:/user.slice\n", ""},
		{"leer", "", ""},
	}
	for _, tc := range cases {
		if got := parseCgroupID(strings.NewReader(tc.content)); got != tc.want {
			t.Errorf("%s: parseCgroupID = %q, erwartet %q", tc.name, got, tc.want)
		}
	}
}

func TestIsSelf(t *testing.T) {
	cases := []struct {
		name        string
		containerID string
		self        string
		want        bool
	}{
		{"volle id identisch", selfID, selfID, true},
		{"hostname als 12-stelliges präfix", selfID, selfID[:12], true},
		{"fremder container", "aaaa1b9c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a", selfID, false},
		{"self leer", selfID, "", false},
		{"self zu kurz", selfID, selfID[:8], false},
		{"gesetzter hostname trifft nichts", selfID, "backup-server", false},
	}
	for _, tc := range cases {
		if got := isSelf(tc.containerID, tc.self); got != tc.want {
			t.Errorf("%s: isSelf = %v, erwartet %v", tc.name, got, tc.want)
		}
	}
}
