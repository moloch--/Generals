//go:build darwin

// GeneralsX @feature Codex 01/08/2026 Connect packaged macOS launchers to their AppKit progress helper.
package progress

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const (
	helperBasename  = "GeneralsX-SFX-Progress"
	helperExitLimit = 2 * time.Second
)

type helperStarter func(path string) (io.WriteCloser, func(), error)

// Open starts the progress helper when the launcher is inside a conventional
// macOS app bundle. Missing, invalid, or unstartable helpers produce a no-op
// Reporter so presentation can never block the native game launch.
func Open() *Reporter {
	executable, err := os.Executable()
	if err != nil {
		return newNoopReporter()
	}
	return openForExecutable(executable, startHelper)
}

func openForExecutable(executable string, start helperStarter) *Reporter {
	helper, err := bundledHelperPath(executable)
	if err != nil || start == nil {
		return newNoopReporter()
	}
	writer, wait, err := start(helper)
	if err != nil {
		return newNoopReporter()
	}
	return newReporter(writer, wait)
}

func bundledHelperPath(executable string) (string, error) {
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	macOSDirectory := filepath.Dir(resolvedExecutable)
	contentsDirectory := filepath.Dir(macOSDirectory)
	if filepath.Base(macOSDirectory) != "MacOS" || filepath.Base(contentsDirectory) != "Contents" {
		return "", errors.New("launcher is not inside a macOS app bundle")
	}

	helper := filepath.Join(contentsDirectory, "Helpers", helperBasename)
	info, err := os.Lstat(helper)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("progress helper is not a real executable file")
	}
	return helper, nil
}

func startHelper(path string) (io.WriteCloser, func(), error) {
	command := exec.Command(path)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	inputFile, ok := input.(*os.File)
	if !ok {
		_ = input.Close()
		return nil, nil, errors.New("progress helper input is not an operating-system pipe")
	}
	if err := syscall.SetNonblock(int(inputFile.Fd()), true); err != nil {
		_ = input.Close()
		return nil, nil, err
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	stop := func() {
		select {
		case <-done:
			return
		default:
		}
		go func() {
			timer := time.NewTimer(helperExitLimit)
			defer timer.Stop()
			select {
			case <-done:
				return
			case <-timer.C:
				_ = command.Process.Kill()
			}
		}()
	}
	return nonblockingPipeWriter{File: inputFile}, stop, nil
}

type nonblockingPipeWriter struct {
	*os.File
}

func (writer nonblockingPipeWriter) Write(contents []byte) (int, error) {
	return syscall.Write(int(writer.Fd()), contents)
}
