//go:build !windows && !plan9 && !solaris && !aix
// +build !windows,!plan9,!solaris,!aix

package bbolt

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// flock acquires an advisory lock on a file descriptor.
func flock(db *DB, exclusive bool, timeout time.Duration) error {
	var t time.Time
	if timeout != 0 {
		t = time.Now()
	}
	fd := db.file.Fd()
	flag := syscall.LOCK_NB
	if exclusive {
		flag |= syscall.LOCK_EX
	} else {
		flag |= syscall.LOCK_SH
	}
	for {
		// Attempt to obtain an exclusive lock.
		err := syscall.Flock(int(fd), flag)
		if err == nil {
			return nil
		} else if err != syscall.EWOULDBLOCK {
			return err
		}

		// If we timed out then return an error.
		if timeout != 0 && time.Since(t) > timeout-flockRetryTimeout {
			return ErrTimeout
		}

		// Wait for a bit and try again.
		time.Sleep(flockRetryTimeout)
	}
}

// funlock releases an advisory lock on a file descriptor.
func funlock(db *DB) error {
	return syscall.Flock(int(db.file.Fd()), syscall.LOCK_UN)
}

// mmap memory maps a DB's data file.
func mmap(db *DB, sz int) error {
	// Map the data file to memory.
	prot := unix.PROT_READ
	if !db.readOnly {
		prot = unix.PROT_READ | unix.PROT_WRITE

		db.ops.writeAt = func(p []byte, off int64) (n int, err error) {
			sz = (int(off) + len(p) + db.pageSize - 1) / db.pageSize * db.pageSize
			if sz > db.filesz {
				if err := db.file.Truncate(int64(sz)); err != nil {
					return 0, fmt.Errorf("file resize error: %s", err)
				}
				/*
					if err := db.file.Sync(); err != nil {
						return 0, fmt.Errorf("file sync error: %s", err)
					}

				*/
				db.filesz = sz
			}
			copy(db.dataref[off:off+int64(len(p))], p)
			err = unix.Msync(db.dataref[off:off+int64(len(p))], unix.MS_SYNC)
			if err != nil {
				return 0, err
			}
			return len(p), nil
		}
	}

	b, err := unix.Mmap(int(db.file.Fd()), 0, sz, prot, unix.MAP_SHARED|db.MmapFlags)
	if err != nil {
		return err
	}

	// Advise the kernel that the mmap is accessed randomly.
	err = unix.Madvise(b, syscall.MADV_RANDOM)
	if err != nil && err != syscall.ENOSYS {
		// Ignore not implemented error in kernel because it still works.
		return fmt.Errorf("madvise: %s", err)
	}

	// Save the original byte slice and convert to a byte array pointer.
	db.dataref = b
	db.data = (*[maxMapSize]byte)(unsafe.Pointer(&b[0]))
	db.datasz = sz
	return nil
}

// munmap unmaps a DB's data file from memory.
func munmap(db *DB) error {
	// Ignore the unmap if we have no mapped data.
	if db.dataref == nil {
		return nil
	}

	// Unmap using the original byte slice.
	err := unix.Munmap(db.dataref)
	db.dataref = nil
	db.data = nil
	db.datasz = 0
	return err
}
