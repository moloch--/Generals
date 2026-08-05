package buildcli

// ProgressPhase identifies a stable, UI-neutral stage of the automated build.
type ProgressPhase string

const (
	ProgressPhasePreflight    ProgressPhase = "preflight"
	ProgressPhaseSource       ProgressPhase = "source"
	ProgressPhaseToolchain    ProgressPhase = "toolchain"
	ProgressPhaseAssets       ProgressPhase = "assets"
	ProgressPhaseOnlineServer ProgressPhase = "online-server"
	ProgressPhaseBuild        ProgressPhase = "build"
	ProgressPhaseComplete     ProgressPhase = "complete"
)

// GeneralsX @feature Codex 05/08/2026 Report structured phases without coupling the builder to a UI toolkit.
// ProgressEvent describes the phase the builder is entering.
type ProgressEvent struct {
	Phase   ProgressPhase `json:"phase"`
	Message string        `json:"message"`
}

// Reporter receives ordered build progress events. Implementations should
// return promptly so reporting cannot stall long-running build work.
type Reporter interface {
	Report(ProgressEvent)
}

// ReporterFunc adapts a function to Reporter.
type ReporterFunc func(ProgressEvent)

// Report implements Reporter.
func (function ReporterFunc) Report(event ProgressEvent) {
	if function != nil {
		function(event)
	}
}

func reportProgress(reporter Reporter, phase ProgressPhase, message string) {
	if reporter != nil {
		reporter.Report(ProgressEvent{Phase: phase, Message: message})
	}
}
