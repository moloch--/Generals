package buildcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrebuiltAppPublicationRejectsUnrelatedDirectory(t *testing.T) {
	t.Parallel()
	app := filepath.Join(t.TempDir(), "Game.app")
	readIdentifier := func(string) (string, error) { return "com.generalsx.generalsxzh.sfx", nil }
	if err := validateExistingGameApp(app, readIdentifier); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateExistingGameApp(app, readIdentifier); err == nil {
		t.Fatal("accepted unrelated directory")
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateExistingGameApp(app, func(string) (string, error) { return "com.other.app", nil }); err == nil {
		t.Fatal("accepted unrelated app")
	}
	if err := validateExistingGameApp(app, func(string) (string, error) { return "", errors.New("invalid plist") }); err == nil {
		t.Fatal("accepted invalid plist")
	}
	if err := validateExistingGameApp(app, readIdentifier); err != nil {
		t.Fatal(err)
	}
}

// GeneralsX @build Codex 10/10/2026 Exercise a late app-publication failure and cancellation after raw SFX replacement.
func TestPrebuiltPublicationRestoresPreviousArtifacts(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"success", "rename-failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			raw, rawNew := filepath.Join(root, "game"), filepath.Join(root, "staged-game")
			app, appNew := filepath.Join(root, "Game.app"), filepath.Join(root, "staged.app")
			for name, contents := range map[string]string{raw: "old", rawNew: "new"} {
				if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for name, contents := range map[string]string{app: "old", appNew: "new"} {
				if err := os.Mkdir(name, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(name, "payload"), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rename := func(from, to string) error {
				if outcome == "rename-failure" && from == appNew {
					return errors.New("injected publication failure")
				}
				err := os.Rename(from, to)
				if outcome == "cancel" && from == rawNew && err == nil {
					cancel()
				}
				return err
			}
			err := publishPrebuiltOutputs(ctx, []prebuiltOutput{{staged: rawNew, target: raw}, {staged: appNew, target: app, directory: true}}, rename)
			want := "old"
			if outcome == "success" {
				want = "new"
			}
			if (err == nil) != (outcome == "success") {
				t.Fatalf("unexpected publication result: %v", err)
			}
			for _, name := range []string{raw, filepath.Join(app, "payload")} {
				got, readErr := os.ReadFile(name)
				if readErr != nil || string(got) != want {
					t.Fatalf("%s = %q, %v; want %s", name, got, readErr, want)
				}
			}
			backups, _ := filepath.Glob(filepath.Join(root, ".prebuilt-previous-*"))
			if len(backups) != 0 {
				t.Fatalf("unexpected remaining backups: %v", backups)
			}
		})
	}
}

func TestPrebuiltPublicationRetainsBackupWhenRestorationFails(t *testing.T) {
	root := t.TempDir()
	old, staged := filepath.Join(root, "game"), filepath.Join(root, "new")
	if err := os.WriteFile(old, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	rename := func(from, to string) error {
		if to == old {
			return errors.New("destination locked")
		}
		return os.Rename(from, to)
	}
	if err := publishPrebuiltOutputs(context.Background(), []prebuiltOutput{{staged: staged, target: old}}, rename); err == nil {
		t.Fatal("accepted failed publication")
	}
	backups, _ := filepath.Glob(filepath.Join(root, ".prebuilt-previous-*", "previous"))
	if len(backups) != 1 {
		t.Fatalf("missing recovery backup: %v", backups)
	}
	contents, err := os.ReadFile(backups[0])
	if err != nil || string(contents) != "original" {
		t.Fatalf("lost original output: %q, %v", contents, err)
	}
}
