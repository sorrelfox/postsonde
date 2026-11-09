//go:build windows

package keychain

import (
	"runtime"
	"syscall"
	"unsafe"
)

// The Data Protection API of crypt32.dll, called through syscall (no
// golang.org/x/sys).
var (
	modCrypt32             = syscall.NewLazyDLL("crypt32.dll")
	modKernel32            = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtectData   = modCrypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = modCrypt32.NewProc("CryptUnprotectData")
	procLocalFree          = modKernel32.NewProc("LocalFree")
)

// cryptprotectUIForbidden: fail rather than show a dialog.
const cryptprotectUIForbidden = 0x1

// dataBlob is DATA_BLOB.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

// take copies the blob Windows allocated, then frees it with LocalFree,
// wiping it first.
func (b *dataBlob) take() []byte {
	if b.pbData == nil {
		return nil
	}
	buf := unsafe.Slice(b.pbData, b.cbData)
	out := append([]byte(nil), buf...)
	clear(buf)
	procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
	b.pbData = nil
	return out
}

func dpapiLoad() error {
	if err := procCryptProtectData.Find(); err != nil {
		return unavailable(dpapiBackend, "The Windows Data Protection API is not available.", fallbackHint)
	}
	if err := procCryptUnprotectData.Find(); err != nil {
		return unavailable(dpapiBackend, "The Windows Data Protection API is not available.", fallbackHint)
	}
	if err := procLocalFree.Find(); err != nil {
		return unavailable(dpapiBackend, "The Windows Data Protection API is not available.", fallbackHint)
	}
	return nil
}

// protectData seals data for the current user (CryptProtectData).
func protectData(data, entropy []byte) ([]byte, error) {
	if err := dpapiLoad(); err != nil {
		return nil, err
	}
	in, ent := newBlob(data), newBlob(entropy)
	var out dataBlob
	descr, err := syscall.UTF16PtrFromString(Label)
	if err != nil {
		return nil, err
	}
	r, _, callErr := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		uintptr(unsafe.Pointer(descr)),
		uintptr(unsafe.Pointer(&ent)),
		0, 0,
		cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if r == 0 {
		return nil, callErr
	}
	return out.take(), nil
}

// unprotectData unseals a blob of protectData (CryptUnprotectData).
func unprotectData(blob, entropy []byte) ([]byte, error) {
	if err := dpapiLoad(); err != nil {
		return nil, err
	}
	in, ent := newBlob(blob), newBlob(entropy)
	var out dataBlob
	r, _, callErr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		uintptr(unsafe.Pointer(&ent)),
		0, 0,
		cryptprotectUIForbidden,
		uintptr(unsafe.Pointer(&out)))
	runtime.KeepAlive(blob)
	runtime.KeepAlive(entropy)
	if r == 0 {
		return nil, callErr
	}
	return out.take(), nil
}
