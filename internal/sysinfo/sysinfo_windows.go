//go:build windows

package sysinfo

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func platformInfo() Platform {
	p := Platform{Uptime: windows.DurationSinceBoot()}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProductName")
		disp, _, _ := k.GetStringValue("DisplayVersion")
		build, _, _ := k.GetStringValue("CurrentBuild")
		k.Close()
		// Windows 11 still reports ProductName "Windows 10".
		if b, err := strconv.Atoi(build); err == nil && b >= 22000 {
			name = strings.Replace(name, "Windows 10", "Windows 11", 1)
		}
		p.OSName = strings.TrimSpace(name + " " + disp)
	}
	if v := windows.RtlGetVersion(); v != nil {
		p.Kernel = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProcessorNameString")
		k.Close()
		p.CPUModel = strings.TrimSpace(name)
	}
	p.UsagePercent = cpuUsage(250 * time.Millisecond)
	return p
}

func systemTimes() (idle, total uint64, ok bool) {
	var i, k, u windows.Filetime
	r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	if r == 0 {
		return 0, 0, false
	}
	ft := func(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	// Kernel time includes idle time.
	return ft(i), ft(k) + ft(u), true
}

// ReadCPUTimes reports cumulative idle and total CPU time.
func ReadCPUTimes() (CPUTimes, bool) {
	idle, total, ok := systemTimes()
	return CPUTimes{Idle: idle, Total: total}, ok
}

func cpuUsage(sample time.Duration) float64 { return sampleCPU(sample) }

func memoryInfo() (Memory, error) {
	ms := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 {
		return Memory{}, err
	}
	m := Memory{TotalBytes: ms.TotalPhys, AvailableBytes: ms.AvailPhys}
	if ms.TotalPageFile > ms.TotalPhys {
		m.SwapTotalBytes = ms.TotalPageFile - ms.TotalPhys
		if ms.AvailPageFile > ms.AvailPhys {
			m.SwapFreeBytes = min(ms.AvailPageFile-ms.AvailPhys, m.SwapTotalBytes)
		}
	}
	m.UsedPercent = usedPercent(m.TotalBytes, m.AvailableBytes)
	return m, nil
}

// DiskUsage reports the volume that holds path (or its nearest existing parent).
func DiskUsage(path string) (Disk, error) {
	dir := existingAncestor(path)
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return Disk{}, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return Disk{}, err
	}
	d := Disk{Path: path, TotalBytes: total, FreeBytes: avail}
	d.UsedPercent = usedPercent(d.TotalBytes, d.FreeBytes)
	return d, nil
}
