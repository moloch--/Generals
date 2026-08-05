//go:build windows

package buildcli

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessJob struct {
	handle    windows.Handle
	closeErr  error
	closeOnce sync.Once
}

func startManagedProcess(command *exec.Cmd) (managedProcess, error) {
	job, err := createKillOnCloseJob()
	if err != nil {
		return nil, err
	}

	configureSuspendedProcess(command)
	if err := command.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}

	if err := assignProcessToJob(command, job); err != nil {
		abortSuspendedProcess(command, job)
		return nil, err
	}
	if err := resumeProcess(command.Process.Pid); err != nil {
		abortSuspendedProcess(command, job)
		return nil, err
	}
	return &windowsProcessJob{handle: job}, nil
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create process job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("configure process job: %w", err)
	}
	return job, nil
}

func configureSuspendedProcess(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
}

func assignProcessToJob(command *exec.Cmd, job windows.Handle) error {
	var assignErr error
	if err := command.Process.WithHandle(func(handle uintptr) {
		assignErr = windows.AssignProcessToJobObject(job, windows.Handle(handle))
	}); err != nil {
		return fmt.Errorf("access external process handle: %w", err)
	}
	if assignErr != nil {
		return fmt.Errorf("assign external process to job: %w", assignErr)
	}
	return nil
}

func resumeProcess(processID int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("enumerate suspended process threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return fmt.Errorf("read suspended process threads: %w", err)
	}
	resumed := false
	for {
		if entry.OwnerProcessID == uint32(processID) {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return fmt.Errorf("open suspended process thread: %w", err)
			}
			_, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			if resumeErr != nil {
				return fmt.Errorf("resume external process thread: %w", resumeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close external process thread: %w", closeErr)
			}
			resumed = true
		}

		err := windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return fmt.Errorf("read suspended process threads: %w", err)
		}
	}
	if !resumed {
		return errors.New("suspended external process has no resumable thread")
	}
	return nil
}

func abortSuspendedProcess(command *exec.Cmd, job windows.Handle) {
	_ = windows.TerminateJobObject(job, 1)
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
	_ = windows.CloseHandle(job)
}

// GeneralsX @build Codex 05/08/2026 Terminate the Windows Job Object so every descendant exits with the build command.
func (job *windowsProcessJob) terminate() error {
	if err := windows.TerminateJobObject(job.handle, 1); err != nil {
		return errors.Join(err, job.closeHandle())
	}
	return nil
}

func (job *windowsProcessJob) closeHandle() error {
	job.closeOnce.Do(func() {
		job.closeErr = windows.CloseHandle(job.handle)
	})
	return job.closeErr
}

func (job *windowsProcessJob) close() error {
	return job.closeHandle()
}
