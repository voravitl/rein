package supervisor

import (
	"errors"
	"os"
	"syscall"
)

func openDAGTimingFile(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Open the reparse point itself, so a replaced symlink cannot create its
	// target. Append-only write access preserves existing sidecar contents.
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.FILE_APPEND_DATA,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var info syscall.ByHandleFileInformation
	if err = syscall.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = syscall.CloseHandle(handle)
		if err == nil {
			err = errors.New("timing sidecar reparse point forbidden")
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func dagTimingSingleLink(f *os.File, _ os.FileInfo) bool {
	var info syscall.ByHandleFileInformation
	return syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info) == nil && info.NumberOfLinks == 1
}
