//go:build windows

package store

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// setRelativeSymlinkReparse writes a SYMLINK reparse buffer with
// SYMLINK_FLAG_RELATIVE set (Flags=1) onto an open, empty directory handle
// — the shape setSymlinkReparse (win32_windows.go) deliberately never
// produces (this store only ever writes Flags=0, "always, for a link WE
// create") but Scope A forgives a link the store did not create, so
// readlinkAt must still refuse it: a relative substitute name is exactly
// what CreateFile would resolve against the process's current working
// directory rather than the link's own directory. Mirrors
// setSymlinkReparse's layout with one bit flipped, rather than reusing it,
// so the buffer this test writes is visibly independent of the production
// function it is testing.
func setRelativeSymlinkReparse(h windows.Handle, sub, print string) error {
	subU := windows.StringToUTF16(sub)
	printU := windows.StringToUTF16(print)
	subBytes, printBytes := (len(subU)-1)*2, (len(printU)-1)*2
	const fixed = 12 // 4 x uint16 + Flags uint32
	pathBytes := subBytes + 2 + printBytes + 2
	buf := make([]byte, 8+fixed+pathBytes)
	put32(buf, 0, windows.IO_REPARSE_TAG_SYMLINK)
	put16(buf, 4, uint16(fixed+pathBytes))
	put16(buf, 6, 0)
	put16(buf, 8, 0)
	put16(buf, 10, uint16(subBytes))
	put16(buf, 12, uint16(subBytes+2))
	put16(buf, 14, uint16(printBytes))
	put32(buf, 16, 1) // SYMLINK_FLAG_RELATIVE
	off := 20
	for _, c := range subU {
		put16(buf, off, c)
		off += 2
	}
	for _, c := range printU {
		put16(buf, off, c)
		off += 2
	}
	var n uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &n, nil)
}

// openEmptyLinkForReparse claims name under parent and reopens it with
// FILE_OPEN_REPARSE_POINT, the same two-step shape symlinkAt uses, so a
// test can stamp an arbitrary reparse buffer onto it (symlinkAt itself
// never writes the shapes these tests need — a relative symlink or a
// volume-mount-point substitute name — since the store never creates
// either).
func openEmptyLinkForReparse(t *testing.T, parent int, name string) windows.Handle {
	t.Helper()
	if err := mkdirClaim(parent, name); err != nil {
		t.Fatalf("mkdirClaim(%q): %v", name, err)
	}
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ|windows.DELETE,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		t.Fatalf("open new link %q: %v", name, err)
	}
	return h
}

// TestReadlinkAtRefusesRelativeSymlink is the direct regression test for
// readlinkAt's SYMLINK_FLAG_RELATIVE refusal (link_windows.go): before this
// file, grep found neither "SYMLINK_FLAG_RELATIVE" nor "relative symlink"
// in any _test.go in the tree (P4.7 semantic-parity finding P-6). The ADR
// carried this refusal as [UNMEASURED]; this is the measurement.
func TestReadlinkAtRefusesRelativeSymlink(t *testing.T) {
	testRoot(t)
	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	defer rfs.close()
	parent := int(rfs.root.Fd())

	h := openEmptyLinkForReparse(t, parent, "relsym")
	if err := setRelativeSymlinkReparse(h, `..\elsewhere`, `..\elsewhere`); err != nil {
		windows.CloseHandle(h)
		t.Fatalf("setRelativeSymlinkReparse: %v", err)
	}
	windows.CloseHandle(h)

	target, err := readlinkAt(parent, "relsym")
	if err == nil {
		t.Fatalf("readlinkAt followed a relative symlink, want refusal; got target %q", target)
	}
	if !strings.Contains(err.Error(), "relative") {
		t.Errorf("readlinkAt error = %v, want it to name the relative-symlink refusal", err)
	}
}

// TestReadlinkAtRefusesVolumeMountPoint is the direct regression test for
// readlinkAt's \??\Volume{ refusal (link_windows.go, ADR §5.3): a volume
// mount point shares IO_REPARSE_TAG_MOUNT_POINT with an ordinary junction —
// only the substitute name distinguishes them — and crossing one would move
// to a different volume with a different security surface this design does
// not evaluate. Before this file, grep found no test for it at all.
func TestReadlinkAtRefusesVolumeMountPoint(t *testing.T) {
	testRoot(t)
	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	defer rfs.close()
	parent := int(rfs.root.Fd())

	h := openEmptyLinkForReparse(t, parent, "volmount")
	// A fabricated but well-formed volume GUID substitute name. readlinkAt's
	// check is a prefix match on the substitute name, so this never needs to
	// resolve to a real volume.
	const volume = `\??\Volume{12345678-1234-1234-1234-123456789abc}\`
	if err := setMountPointReparse(h, volume, volume); err != nil {
		windows.CloseHandle(h)
		t.Fatalf("setMountPointReparse: %v", err)
	}
	windows.CloseHandle(h)

	target, err := readlinkAt(parent, "volmount")
	if err == nil {
		t.Fatalf("readlinkAt followed a volume mount point, want refusal; got target %q", target)
	}
	if !strings.Contains(err.Error(), "volume mount point") {
		t.Errorf("readlinkAt error = %v, want it to name the volume-mount-point refusal", err)
	}
}

// TestReadlinkAtAcceptsOrdinaryJunction is the negative control for both
// tests above: an ordinary junction (MOUNT_POINT tag, absolute substitute
// name, no Volume{ prefix) must still be followed normally, proving the two
// refusals above are narrow rather than accidentally rejecting every
// MOUNT_POINT.
func TestReadlinkAtAcceptsOrdinaryJunction(t *testing.T) {
	testRoot(t)
	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	defer rfs.close()
	parent := int(rfs.root.Fd())

	junctionTarget := t.TempDir()
	if err := makeJunctionAt(parent, "ordinary", junctionTarget); err != nil {
		t.Fatalf("makeJunctionAt: %v", err)
	}

	target, err := readlinkAt(parent, "ordinary")
	if err != nil {
		t.Fatalf("readlinkAt refused an ordinary junction: %v", err)
	}
	if !strings.EqualFold(target, junctionTarget) {
		t.Errorf("readlinkAt(ordinary junction) = %q, want %q", target, junctionTarget)
	}
}
