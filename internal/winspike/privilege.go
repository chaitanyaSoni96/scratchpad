//go:build windows

package winspike

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// GitHub-hosted Windows runners execute ELEVATED (Administrator) with
// Developer Mode enabled. That makes every "can an unprivileged process do X"
// question unanswerable by simply trying it: the token already holds
// SeCreateSymbolicLinkPrivilege. RemovePrivilege strips the privilege from the
// CURRENT PROCESS token irreversibly (SE_PRIVILEGE_REMOVED), which is why the
// probes that use it run in a re-executed child process.

const seCreateSymbolicLinkName = "SeCreateSymbolicLinkPrivilege"

// HasPrivilege reports whether the process token holds the named privilege at
// all (enabled or not).
func HasPrivilege(name string) (bool, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false, err
	}
	defer token.Close()

	var want windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &want); err != nil {
		return false, err
	}
	var size uint32
	windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size)
	if size == 0 {
		return false, fmt.Errorf("winspike: GetTokenInformation returned no size")
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buf[0], size, &size); err != nil {
		return false, err
	}
	tp := (*windows.Tokenprivileges)(unsafePointer(&buf[0]))
	for _, p := range tp.AllPrivileges() {
		if p.Luid == want {
			return true, nil
		}
	}
	return false, nil
}

// RemovePrivilege removes the named privilege from the current process token.
// SE_PRIVILEGE_REMOVED is permanent for the life of the process, which is what
// makes the resulting measurement trustworthy: CreateSymbolicLinkW enables the
// privilege on demand, so merely DISABLING it would prove nothing.
func RemovePrivilege(name string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()

	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_REMOVED}
	return windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil)
}
