//go:build windows

package settings

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

// SystemPolicyPath returns where the admin policy file is looked for:
// %ProgramData%\PostSonde\policy.json, which only administrators can
// write. The ProgramData folder is asked of Windows (SHGetKnownFolderPath,
// which reads it from the machine's registry), not taken from the
// ProgramData environment variable, which a user can set for a process to
// get around the policy. If Windows cannot say, C:\ProgramData is used.
func SystemPolicyPath() string {
	return filepath.Join(programDataDir(), "PostSonde", PolicyFileName)
}

var (
	modShell32               = syscall.NewLazyDLL("shell32.dll")
	modOle32                 = syscall.NewLazyDLL("ole32.dll")
	procSHGetKnownFolderPath = modShell32.NewProc("SHGetKnownFolderPath")
	procCoTaskMemFree        = modOle32.NewProc("CoTaskMemFree")
)

// folderIDProgramData is FOLDERID_ProgramData,
// {62AB5D82-FDC1-4DC3-A9DD-070D1D495D97}.
var folderIDProgramData = syscall.GUID{
	Data1: 0x62AB5D82, Data2: 0xFDC1, Data3: 0x4DC3,
	Data4: [8]byte{0xA9, 0xDD, 0x07, 0x0D, 0x1D, 0x49, 0x5D, 0x97},
}

const fallbackProgramData = `C:\ProgramData`

func programDataDir() string {
	if procSHGetKnownFolderPath.Find() != nil || procCoTaskMemFree.Find() != nil {
		return fallbackProgramData
	}
	var p *uint16
	hr, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&folderIDProgramData)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if p == nil {
		return fallbackProgramData
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	if hr != 0 {
		return fallbackProgramData
	}
	n := 0
	for q := unsafe.Pointer(p); *(*uint16)(q) != 0; q = unsafe.Add(q, 2) {
		n++
	}
	dir := syscall.UTF16ToString(unsafe.Slice(p, n))
	if !filepath.IsAbs(dir) {
		return fallbackProgramData
	}
	return dir
}
