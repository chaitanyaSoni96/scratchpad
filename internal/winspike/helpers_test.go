//go:build windows

package winspike

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// scratchDir is a temp directory whose cleanup is best-effort. It is
// deliberately NOT t.TempDir(): these tests plant junctions, symlinks,
// delete-pending files and unknown reparse tags, and a cleanup failure must
// not turn a measurement into a test failure.
func scratchDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "winspike-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func openScratchRoot(t *testing.T) (*Root, string) {
	t.Helper()
	dir := scratchDir(t)
	r, err := OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot(%q): %v", dir, DescribeErr(err))
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, dir
}

var (
	symlinkOnce sync.Once
	symlinkOK   bool
	symlinkErr  error
)

// symlinkCapability probes once whether this process can create a directory
// symbolic link with SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE.
func symlinkCapability(t *testing.T) (bool, error) {
	t.Helper()
	symlinkOnce.Do(func() {
		d, err := os.MkdirTemp("", "winspike-symprobe-")
		if err != nil {
			symlinkErr = err
			return
		}
		defer os.RemoveAll(d)
		target := filepath.Join(d, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			symlinkErr = err
			return
		}
		symlinkErr = CreateDirSymlink(filepath.Join(d, "link"), target, true)
		symlinkOK = symlinkErr == nil
	})
	return symlinkOK, symlinkErr
}

// ntdll entry points x/sys/windows does not wrap.
var (
	ntdll                    = windows.NewLazySystemDLL("ntdll.dll")
	procRtlIsDosDeviceNameU  = ntdll.NewProc("RtlIsDosDeviceName_U")
	procRtlGetVersion        = ntdll.NewProc("RtlGetVersion")
	procNtSetInformationFile = ntdll.NewProc("NtSetInformationFile")
)

// rtlIsDosDeviceName returns non-zero when the OS considers name a DOS device.
// This is the authoritative test Go's isReservedName defers to, and the answer
// changed for "CON.txt"-style names in Windows 11 (threat model §4.12, M18).
func rtlIsDosDeviceName(name string) uint32 {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	r, _, _ := procRtlIsDosDeviceNameU.Call(uintptr(unsafe.Pointer(p)))
	return uint32(r)
}

type osVersionInfoEx struct {
	OSVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformId        uint32
	CSDVersion        [128]uint16
	ServicePackMajor  uint16
	ServicePackMinor  uint16
	SuiteMask         uint16
	ProductType       byte
	Reserved          byte
}

// osBuild reports the real build number. RtlGetVersion is not subject to the
// compatibility shims GetVersionEx is, so it is the right instrument for
// "minimum OS build" questions (M1, M9, M10).
func osBuild() (major, minor, build uint32) {
	var vi osVersionInfoEx
	vi.OSVersionInfoSize = uint32(unsafe.Sizeof(vi))
	procRtlGetVersion.Call(uintptr(unsafe.Pointer(&vi)))
	return vi.MajorVersion, vi.MinorVersion, vi.BuildNumber
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	return path
}

func mustWrite(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}
