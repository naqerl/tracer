//go:build linux

package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func (fs *fileSource) Open(context.Context) error {
	if err := fs.validateConfig(); err != nil {
		return err
	}

	if err := fs.openCurrent(strings.EqualFold(fs.from, readFromEnd)); err != nil {
		return err
	}
	if err := fs.startWatcher(); err != nil {
		_ = fs.file.Close()
		fs.file = nil
		fs.reader = nil
		fs.partial = ""
		fs.offset = 0
		return err
	}
	return nil
}

func (fs *fileSource) Next(ctx context.Context) (string, error) {
	if fs.reader == nil {
		return "", fmt.Errorf("file source not open")
	}

	for {
		line, ok, err := fs.readNextLine()
		if err != nil {
			return "", err
		}
		if ok {
			return line, nil
		}

		// After a seek the caller wants a bounded read of the file's current
		// contents; report EOF instead of waiting for appends.
		if fs.seeked {
			return "", io.EOF
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-fs.errs:
			return "", err
		case <-fs.events:
			if err := fs.syncFileWithPath(); err != nil {
				return "", err
			}
		}
	}
}

func (fs *fileSource) Close() error {
	if fs.cancelW >= 0 {
		_, _ = unix.Write(fs.cancelW, []byte{1})
	}
	fs.watchWG.Wait()
	if fs.cancelR >= 0 {
		_ = unix.Close(fs.cancelR)
		fs.cancelR = -1
	}
	if fs.cancelW >= 0 {
		_ = unix.Close(fs.cancelW)
		fs.cancelW = -1
	}
	if fs.watchFD >= 0 {
		_ = unix.Close(fs.watchFD)
		fs.watchFD = -1
	}

	var closeErr error
	if fs.file != nil {
		if err := fs.file.Close(); err != nil {
			closeErr = err
		}
		fs.file = nil
	}
	fs.reader = nil
	fs.partial = ""
	fs.offset = 0
	fs.dirWD = 0
	fs.fileWD = 0
	fs.cancelR = -1
	fs.cancelW = -1
	fs.events = nil
	fs.errs = nil
	return closeErr
}

// Seek implements io.Seeker for fileSource.
// It allows seeking to a specific byte offset in the file.
// Note: Seeking resets the internal buffer, discarding any partial line.
// For io.SeekCurrent with offset=0, returns the logical position without
// affecting the buffer.
func (fs *fileSource) Seek(offset int64, whence int) (int64, error) {
	if fs.file == nil {
		return 0, fmt.Errorf("file source not open")
	}

	// For io.SeekCurrent with offset=0, return the logical position we track.
	// We can't use fs.file.Seek() because bufio.Reader reads ahead into its buffer,
	// so the actual file position would be ahead of the logical position.
	if whence == io.SeekCurrent && offset == 0 {
		return fs.offset, nil
	}

	var targetOffset int64
	var err error

	switch whence {
	case io.SeekStart, io.SeekEnd:
		// For absolute seeks, use the underlying file directly
		targetOffset, err = fs.file.Seek(offset, whence)
	case io.SeekCurrent:
		// For relative seeks, calculate absolute position based on our logical offset
		targetOffset = fs.offset + offset
		targetOffset, err = fs.file.Seek(targetOffset, io.SeekStart)
	default:
		return 0, fmt.Errorf("file source seek (%s): invalid whence %d", fs.path, whence)
	}

	if err != nil {
		return 0, fmt.Errorf("file source seek (%s): %w", fs.path, err)
	}

	// Reset the buffered reader and clear partial line buffer
	fs.reader.Reset(fs.file)
	fs.partial = ""
	fs.offset = targetOffset
	fs.seeked = true

	return targetOffset, nil
}

func (fs *fileSource) validateConfig() error {
	if strings.TrimSpace(fs.path) == "" {
		return fmt.Errorf("file source: empty path")
	}
	if fs.from == "" {
		fs.from = readFromStart
	}
	if fs.from != readFromStart && fs.from != readFromEnd {
		return fmt.Errorf("file source: invalid read_from %q (expected %q or %q)", fs.from, readFromStart, readFromEnd)
	}
	return nil
}

func (fs *fileSource) openCurrent(seekEnd bool) error {
	file, err := os.Open(fs.path)
	if err != nil {
		return fmt.Errorf("file source open (%s): %w", fs.path, err)
	}

	offset := int64(0)
	if seekEnd {
		offset, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("file source seek end (%s): %w", fs.path, err)
		}
	}

	if fs.file != nil {
		_ = fs.file.Close()
	}
	fs.file = file
	fs.reader = bufio.NewReader(file)
	fs.partial = ""
	fs.offset = offset

	if fs.watchFD >= 0 {
		if err := fs.refreshFileWatch(); err != nil {
			return err
		}
	}
	return nil
}

func (fs *fileSource) startWatcher() error {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("file source watcher init (%s): %w", fs.path, err)
	}

	dir := filepath.Dir(fs.path)
	dirMask := uint32(unix.IN_CREATE | unix.IN_MOVED_TO | unix.IN_ATTRIB | unix.IN_DELETE | unix.IN_DELETE_SELF)
	dirWD, err := unix.InotifyAddWatch(fd, dir, dirMask)
	if err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("file source watch dir (%s): %w", dir, err)
	}

	fs.watchFD = fd
	fs.dirWD = dirWD
	fs.fileWD = 0
	if err := fs.refreshFileWatch(); err != nil {
		_ = unix.Close(fd)
		fs.watchFD = -1
		fs.dirWD = 0
		return err
	}

	fs.events = make(chan struct{}, 1)
	fs.errs = make(chan error, 1)
	pipeFds := []int{0, 0}
	if err := unix.Pipe2(pipeFds, unix.O_CLOEXEC); err != nil {
		_ = unix.Close(fs.watchFD)
		fs.watchFD = -1
		fs.dirWD = 0
		fs.fileWD = 0
		return fmt.Errorf("file source watcher cancel pipe (%s): %w", fs.path, err)
	}
	fs.cancelR = pipeFds[0]
	fs.cancelW = pipeFds[1]
	fs.watchWG.Add(1)
	go fs.watchLoop()
	return nil
}

func (fs *fileSource) refreshFileWatch() error {
	if fs.watchFD < 0 {
		return fmt.Errorf("file source watcher not initialized")
	}
	if fs.fileWD > 0 {
		if _, err := unix.InotifyRmWatch(fs.watchFD, uint32(fs.fileWD)); err != nil && !errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("file source remove watch (%s): %w", fs.path, err)
		}
		fs.fileWD = 0
	}

	fileMask := uint32(unix.IN_MODIFY | unix.IN_CLOSE_WRITE | unix.IN_ATTRIB | unix.IN_MOVE_SELF | unix.IN_DELETE_SELF)
	wd, err := unix.InotifyAddWatch(fs.watchFD, fs.path, fileMask)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("file source watch file (%s): %w", fs.path, err)
	}
	fs.fileWD = wd
	return nil
}

func (fs *fileSource) watchLoop() {
	defer fs.watchWG.Done()
	buf := make([]byte, 4096)
	pollFds := []unix.PollFd{
		{Fd: int32(fs.watchFD), Events: unix.POLLIN},
		{Fd: int32(fs.cancelR), Events: unix.POLLIN},
	}

	for {
		_, err := unix.Poll(pollFds, -1)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.EBADF) {
				return
			}
			select {
			case fs.errs <- fmt.Errorf("file source watcher poll (%s): %w", fs.path, err):
			default:
			}
			return
		}
		if pollFds[1].Revents&unix.POLLIN != 0 {
			return
		}
		if pollFds[0].Revents&(unix.POLLIN|unix.POLLERR|unix.POLLHUP) == 0 {
			continue
		}

		n, err := unix.Read(fs.watchFD, buf)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			if errors.Is(err, unix.EBADF) {
				return
			}
			select {
			case fs.errs <- fmt.Errorf("file source watcher read (%s): %w", fs.path, err):
			default:
			}
			return
		}
		if n <= 0 {
			continue
		}
		select {
		case fs.events <- struct{}{}:
		default:
		}
	}
}

func (fs *fileSource) readNextLine() (string, bool, error) {
	for {
		line, err := fs.reader.ReadString('\n')
		fs.offset += int64(len(line))

		switch {
		case err == nil:
			if fs.partial != "" {
				line = fs.partial + line
				fs.partial = ""
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				continue
			}
			return line, true, nil
		case errors.Is(err, io.EOF):
			if line != "" {
				fs.partial += line
			}
			return "", false, nil
		default:
			return "", false, fmt.Errorf("file source read (%s): %w", fs.path, err)
		}
	}
}

func (fs *fileSource) syncFileWithPath() error {
	if fs.file == nil {
		return fmt.Errorf("file source not open")
	}

	pathInfo, err := os.Stat(fs.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("file source stat path (%s): %w", fs.path, err)
	}

	currentInfo, err := fs.file.Stat()
	if err != nil {
		return fmt.Errorf("file source stat current (%s): %w", fs.path, err)
	}

	if !os.SameFile(pathInfo, currentInfo) {
		if err := fs.openCurrent(false); err != nil {
			return err
		}
		return nil
	}

	if pathInfo.Size() < fs.offset {
		offset, err := fs.file.Seek(0, io.SeekStart)
		if err != nil {
			return fmt.Errorf("file source seek start (%s): %w", fs.path, err)
		}
		fs.reader.Reset(fs.file)
		fs.partial = ""
		fs.offset = offset
	}

	return nil
}
