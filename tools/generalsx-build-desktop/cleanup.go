package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const cleanupVerifierOutputLimit = 64 * 1024

type preparedCleanupPlan struct {
	planID          string
	sourceReceipt   *buildCleanupReceipt
	receipt         *buildCleanupReceipt
	desktopArtifact *completedArtifact
}

// GeneralsX @feature Codex 05/08/2026 Preview only same-job, creation-receipted paths before destructive cleanup.
func (a *App) GetBuildCleanupPlan(jobID string) (BuildCleanupPlan, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return BuildCleanupPlan{}, errors.New("build job ID is required")
	}

	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return BuildCleanupPlan{}, errors.New("desktop application is shutting down")
	}
	if a.active != nil {
		a.mu.Unlock()
		return BuildCleanupPlan{}, errors.New("a build is still running")
	}
	if a.copyInProgress || a.cleanupInProgress || a.cleanupPlanning {
		a.mu.Unlock()
		return BuildCleanupPlan{}, errors.New("another post-build action is still running")
	}
	receipt := a.cleanupReceipt
	desktopArtifact := a.desktopArtifact
	if receipt == nil || receipt.jobID != jobID {
		a.mu.Unlock()
		return BuildCleanupPlan{}, errors.New("no cleanup receipt is available for this build")
	}
	if desktopArtifact == nil || desktopArtifact.jobID != jobID {
		a.mu.Unlock()
		return BuildCleanupPlan{}, errors.New("copy the verified SFX to Desktop before cleaning up build files")
	}
	a.cleanupPlanning = true
	a.preparedCleanup = nil
	a.mu.Unlock()

	if err := revalidateCompletedArtifact(a.ctx, desktopArtifact); err != nil {
		a.mu.Lock()
		a.cleanupPlanning = false
		a.mu.Unlock()
		return BuildCleanupPlan{}, fmt.Errorf("revalidate Desktop SFX for cleanup review: %w", err)
	}
	plan, preparedReceipt, err := prepareBuildCleanup(receipt, desktopArtifact)
	if err != nil {
		a.mu.Lock()
		a.cleanupPlanning = false
		a.mu.Unlock()
		return BuildCleanupPlan{}, fmt.Errorf("prepare build cleanup: %w", err)
	}
	plan.PlanID = "cleanup-" + generateJobID()

	a.mu.Lock()
	a.cleanupPlanning = false
	if a.shuttingDown || a.active != nil || a.copyInProgress || a.cleanupInProgress ||
		a.cleanupReceipt != receipt || a.desktopArtifact != desktopArtifact {
		a.mu.Unlock()
		return BuildCleanupPlan{}, errors.New("the completed build changed while its cleanup plan was prepared")
	}
	a.preparedCleanup = &preparedCleanupPlan{
		planID: plan.PlanID, sourceReceipt: receipt, receipt: preparedReceipt, desktopArtifact: desktopArtifact,
	}
	a.mu.Unlock()
	return plan, nil
}

// GeneralsX @feature Codex 05/08/2026 Delete only unchanged, same-job owned paths after re-verifying the Desktop SFX.
func (a *App) CleanupBuild(jobID, planID string) (string, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return "", errors.New("build job ID is required")
	}
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return "", errors.New("cleanup plan ID is required")
	}

	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return "", errors.New("desktop application is shutting down")
	}
	if a.active != nil {
		a.mu.Unlock()
		return "", errors.New("a build is still running")
	}
	if a.copyInProgress {
		a.mu.Unlock()
		return "", errors.New("the SFX artifact is still being copied to Desktop")
	}
	if a.cleanupPlanning {
		a.mu.Unlock()
		return "", errors.New("a cleanup plan is still being prepared")
	}
	if a.cleanupInProgress {
		a.mu.Unlock()
		return "", errors.New("build cleanup is already running")
	}
	receipt := a.cleanupReceipt
	desktopArtifact := a.desktopArtifact
	prepared := a.preparedCleanup
	if receipt == nil || receipt.jobID != jobID {
		a.mu.Unlock()
		return "", errors.New("no cleanup receipt is available for this build")
	}
	if desktopArtifact == nil || desktopArtifact.jobID != jobID {
		a.mu.Unlock()
		return "", errors.New("copy the verified SFX to Desktop before cleaning up build files")
	}
	if prepared == nil || prepared.planID != planID || prepared.sourceReceipt != receipt ||
		prepared.desktopArtifact != desktopArtifact || prepared.receipt == nil {
		a.mu.Unlock()
		return "", errors.New("the cleanup plan expired or does not match this build; review cleanup again")
	}
	verifyArtifact := a.dependencies.verifyArtifact
	cleanupBuild := a.dependencies.cleanupBuild
	if verifyArtifact == nil || cleanupBuild == nil {
		a.mu.Unlock()
		return "", errors.New("build cleanup is unavailable")
	}
	cleanupContext, cancel := context.WithCancel(a.ctx)
	done := make(chan struct{})
	a.cleanupInProgress = true
	a.preparedCleanup = nil
	a.cleanupCancel = cancel
	a.cleanupDone = done
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		if a.cleanupDone == done {
			a.cleanupInProgress = false
			a.cleanupCancel = nil
			a.cleanupDone = nil
		}
		a.mu.Unlock()
		close(done)
	}()

	if err := revalidateCompletedArtifact(cleanupContext, desktopArtifact); err != nil {
		return "", fmt.Errorf("revalidate Desktop SFX before cleanup: %w", err)
	}
	if err := verifyArtifact(cleanupContext, desktopArtifact.sourcePath, prepared.receipt.target); err != nil {
		return "", fmt.Errorf("verify Desktop SFX before cleanup: %w", err)
	}
	if err := revalidateCompletedArtifact(cleanupContext, desktopArtifact); err != nil {
		return "", fmt.Errorf("revalidate Desktop SFX after verification: %w", err)
	}
	result, err := cleanupBuild(cleanupContext, prepared.receipt)
	if err != nil {
		return "", fmt.Errorf("clean up build files: %w", err)
	}
	discardBuildCleanupReceipt(receipt)
	postCleanupErr := revalidateCompletedArtifact(cleanupContext, desktopArtifact)

	a.mu.Lock()
	if a.cleanupReceipt == receipt {
		a.cleanupReceipt = nil
		a.completedArtifact = nil
	}
	a.mu.Unlock()
	if postCleanupErr != nil {
		return "", fmt.Errorf("cleanup completed, but the Desktop SFX could not be revalidated: %w", postCleanupErr)
	}
	return result, nil
}
