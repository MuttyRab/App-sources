//go:build windows

package winui

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The modern Windows file dialogs (IFileOpenDialog / IFileSaveDialog), called
// through their COM vtables. Must run on an STA thread.

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	clsidFileSaveDialog = windows.GUID{Data1: 0xC0B4E2F3, Data2: 0xBA21, Data3: 0x4773, Data4: [8]byte{0x8D, 0xBA, 0x33, 0x5E, 0xC9, 0x46, 0xEB, 0x8B}}
	iidIFileSaveDialog  = windows.GUID{Data1: 0x84BCCD23, Data2: 0x5FDE, Data3: 0x4CDB, Data4: [8]byte{0xAE, 0xA4, 0xAF, 0x64, 0xB8, 0x3D, 0x78, 0xAB}}
)

// IFileDialog vtable slots (IUnknown 0-2, IModalWindow 3).
const (
	vRelease                 = 2
	vShow                    = 3
	vSetFileTypes            = 4
	vSetOptions              = 9
	vGetOptions              = 10
	vSetFileName             = 15
	vSetTitle                = 17
	vGetResult               = 20
	vSetDefaultExt           = 22
	vShellItemGetDisplayName = 5
)

const (
	fosOverwritePrompt = 0x2
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosPathMustExist   = 0x800
	fosFileMustExist   = 0x1000
	sigdnFileSysPath   = 0x80058000
	hrCancelled        = 0x800704C7
	clsctxInprocServer = 1
)

type dialogOptions struct {
	title    string
	filters  [][2]string
	folder   bool
	save     bool
	fileName string
	defExt   string
}

type filterSpec struct {
	name *uint16
	spec *uint16
}

func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

// fileDialog shows an open/save/folder dialog and returns the chosen path,
// or "" if the user cancelled.
func fileDialog(owner uintptr, o dialogOptions) (string, error) {
	clsid, iid := &clsidFileOpenDialog, &iidIFileOpenDialog
	if o.save {
		clsid, iid = &clsidFileSaveDialog, &iidIFileSaveDialog
	}
	var dlg unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&dlg)))
	if failed(hr) || dlg == nil {
		return "", fmt.Errorf("couldn't open the file dialog (0x%08X)", uint32(hr))
	}
	defer comCall(dlg, vRelease)

	var opts uint32
	comCall(dlg, vGetOptions, uintptr(unsafe.Pointer(&opts)))
	opts |= fosForceFileSystem | fosPathMustExist
	switch {
	case o.folder:
		opts |= fosPickFolders
	case o.save:
		opts |= fosOverwritePrompt
	default:
		opts |= fosFileMustExist
	}
	comCall(dlg, vSetOptions, uintptr(opts))

	if len(o.filters) > 0 && !o.folder {
		specs := make([]filterSpec, len(o.filters))
		for i, f := range o.filters {
			specs[i] = filterSpec{windows.StringToUTF16Ptr(f[0]), windows.StringToUTF16Ptr(f[1])}
		}
		comCall(dlg, vSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
	}
	if o.title != "" {
		comCall(dlg, vSetTitle, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(o.title))))
	}
	if o.fileName != "" {
		comCall(dlg, vSetFileName, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(o.fileName))))
	}
	if o.defExt != "" {
		comCall(dlg, vSetDefaultExt, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(o.defExt))))
	}

	hr = comCall(dlg, vShow, owner)
	if uint32(hr) == hrCancelled {
		return "", nil
	}
	if failed(hr) {
		return "", fmt.Errorf("the file dialog failed (0x%08X)", uint32(hr))
	}
	var item unsafe.Pointer
	if hr = comCall(dlg, vGetResult, uintptr(unsafe.Pointer(&item))); failed(hr) || item == nil {
		return "", fmt.Errorf("couldn't read the selection (0x%08X)", uint32(hr))
	}
	defer comCall(item, vRelease)
	var psz *uint16
	if hr = comCall(item, vShellItemGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&psz))); failed(hr) || psz == nil {
		return "", fmt.Errorf("that location isn't a folder on this PC")
	}
	path := windows.UTF16PtrToString(psz)
	windows.CoTaskMemFree(unsafe.Pointer(psz))
	return path, nil
}
