package cli

import (
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/sysinfo"
)

func TestStatusShowsSystemAndDataDiskUsage(t *testing.T) {
	info := sysinfo.Info{Disks: []sysinfo.Disk{
		{Label: "system", Path: "/", TotalBytes: 100 << 30, FreeBytes: 25 << 30},
		{Label: "state,backups", Path: "/var/lib/qabas", TotalBytes: 200 << 30, FreeBytes: 50 << 30},
	}}
	m := summarizeMachine(info)
	if len(m.Disks) != 2 || m.DiskTotalBytes != 200<<30 || m.DiskFreeBytes != 50<<30 {
		t.Fatalf("disk summary: %+v", m)
	}
	rows := m.rows()
	var disks []string
	for _, row := range rows {
		if strings.HasPrefix(row[0], "Disk (") {
			disks = append(disks, strings.Join(row, " "))
		}
	}
	if len(disks) != 2 || !strings.Contains(disks[0], "75.0%") || !strings.Contains(disks[0], "25.0 GiB available") || !strings.Contains(disks[0], " — /") || !strings.Contains(disks[1], "state,backups") {
		t.Fatalf("disk rows: %q", disks)
	}
}
