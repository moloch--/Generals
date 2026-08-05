//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type terminalProcess struct {
	command *exec.Cmd
	job     windows.Handle
}

func startTerminalProcess(command *exec.Cmd) (*terminalProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create process job: %w", err)
	}
	closeJob := true
	defer func() {
		if closeJob {
			_ = windows.CloseHandle(job)
		}
	}()
	limits := windowsTerminalJobLimits()
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		return nil, fmt.Errorf("configure process job: %w", err)
	}
	// Start suspended so the command cannot create an untracked descendant in
	// the small interval before it enters the kill-on-close Job Object.
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windowsTerminalProcessCreationFlags(),
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	// GeneralsX @bugfix Codex 05/08/2026 Assign suspended terminals without the Go 1.26-only os.Process handle API.
	processHandle, err := windows.OpenProcess(
		windowsTerminalJobAssignmentProcessAccess(),
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		abortTerminalProcess(command, job)
		return nil, fmt.Errorf("open interactive process for job assignment: %w", err)
	}
	assignErr := windows.AssignProcessToJobObject(job, processHandle)
	closeErr := windows.CloseHandle(processHandle)
	if assignErr != nil {
		abortTerminalProcess(command, job)
		return nil, fmt.Errorf("assign interactive process to job: %w", assignErr)
	}
	if closeErr != nil {
		abortTerminalProcess(command, job)
		return nil, fmt.Errorf("close interactive process assignment handle: %w", closeErr)
	}
	if err := resumeTerminalProcess(command.Process.Pid); err != nil {
		abortTerminalProcess(command, job)
		return nil, err
	}
	closeJob = false
	return &terminalProcess{command: command, job: job}, nil
}

func windowsTerminalJobLimits() windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION {
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	return limits
}

func windowsTerminalProcessCreationFlags() uint32 {
	return windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED
}

func windowsTerminalJobAssignmentProcessAccess() uint32 {
	return windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
}

func resumeTerminalProcess(processID int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("enumerate interactive process threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return fmt.Errorf("read interactive process threads: %w", err)
	}
	resumed := false
	for {
		if entry.OwnerProcessID == uint32(processID) {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return fmt.Errorf("open interactive process thread: %w", err)
			}
			_, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			if resumeErr != nil {
				return fmt.Errorf("resume interactive process thread: %w", resumeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close interactive process thread: %w", closeErr)
			}
			resumed = true
		}

		err := windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return fmt.Errorf("read interactive process threads: %w", err)
		}
	}
	if !resumed {
		return errors.New("suspended interactive process has no resumable thread")
	}
	return nil
}

func abortTerminalProcess(command *exec.Cmd, job windows.Handle) {
	_ = windows.TerminateJobObject(job, 1)
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
}

func (process *terminalProcess) wait() error {
	return process.command.Wait()
}

func (process *terminalProcess) terminate() error {
	return windows.TerminateJobObject(process.job, 130)
}

func (process *terminalProcess) close() error {
	if process.job == 0 {
		return nil
	}
	err := windows.CloseHandle(process.job)
	process.job = 0
	return err
}
