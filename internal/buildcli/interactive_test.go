package buildcli

import (
	"context"
	"io"
	"reflect"
	"testing"
)

func TestRunInteractiveClassifiesDependencyInstaller(t *testing.T) {
	t.Parallel()
	var received InteractiveCommand
	app := application{
		runner: runner{stdout: io.Discard, stderr: io.Discard},
		interactiveRunner: InteractiveCommandRunnerFunc(func(_ context.Context, command InteractiveCommand) error {
			received = command
			return nil
		}),
	}
	spec := command{
		name: "/usr/bin/sudo",
		args: []string{"apt-get", "update"},
		dir:  "/private/work",
		env:  map[string]string{"DEBIAN_FRONTEND": "dialog"},
	}

	if err := app.runInteractive(context.Background(), spec, InteractiveDependencyInstallation); err != nil {
		t.Fatalf("runInteractive() = %v", err)
	}
	if received.Purpose != InteractiveDependencyInstallation {
		t.Fatalf("purpose = %q, want %q", received.Purpose, InteractiveDependencyInstallation)
	}
	if received.Executable != spec.name || received.WorkingDirectory != spec.dir {
		t.Fatalf("interactive command = %#v", received)
	}
	if !reflect.DeepEqual(received.Arguments, spec.args) || !reflect.DeepEqual(received.Environment, spec.env) {
		t.Fatalf("interactive arguments/environment = %#v", received)
	}

	// The bridge receives owned copies so callers cannot mutate an in-flight job.
	spec.args[0] = "changed"
	spec.env["DEBIAN_FRONTEND"] = "changed"
	if received.Arguments[0] != "apt-get" || received.Environment["DEBIAN_FRONTEND"] != "dialog" {
		t.Fatalf("interactive command aliases source storage: %#v", received)
	}
}
