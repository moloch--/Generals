package buildcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type prebuiltOutput struct {
	staged, target string
	directory      bool
}

// Preserve the macOS wrapper's ownership check when publishing its staged app.
func validateExistingGameApp(name string, bundleIdentifier func(string) (string, error)) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("refusing non-directory or symlink app output: %s", name)
	}
	contents := filepath.Join(name, "Contents")
	info, err = os.Lstat(contents)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("refusing to replace an unrecognized app bundle: %s", name)
	}
	plist := filepath.Join(contents, "Info.plist")
	if err := requireRegularFile(plist); err != nil {
		return fmt.Errorf("refusing to replace an unrecognized app bundle: %s: %w", name, err)
	}
	identifier, err := bundleIdentifier(plist)
	if err != nil || identifier != "com.generalsx.generalsxzh.sfx" {
		return fmt.Errorf("refusing to replace an app that is not com.generalsx.generalsxzh.sfx: %s", name)
	}
	return nil
}

// GeneralsX @bugfix Codex 10/10/2026 Prepare every artifact before replacing outputs and restore prior outputs on publication failure.
func publishPrebuiltOutputs(ctx context.Context, outputs []prebuiltOutput, rename func(string, string) error) error {
	type publication struct {
		prebuiltOutput
		previous  os.FileInfo
		backup    string
		movedOld  bool
		published bool
	}
	items := make([]publication, len(outputs))
	for i, output := range outputs {
		items[i].prebuiltOutput = output
		for _, name := range []string{output.staged, output.target} {
			info, err := os.Lstat(name)
			if errors.Is(err, os.ErrNotExist) && name == output.target {
				continue
			}
			if err != nil {
				return err
			}
			if (output.directory && !info.IsDir()) || (!output.directory && !info.Mode().IsRegular()) {
				return fmt.Errorf("unexpected output type: %s", name)
			}
			if name == output.target {
				items[i].previous = info
			}
		}
	}
	// Roll back in reverse order. A failed restoration retains its backup and
	// reports its location instead of deleting the only remaining original.
	rollback := func(cause error) error {
		for i := len(items) - 1; i >= 0; i-- {
			item := &items[i]
			if item.published {
				if err := rename(item.target, item.staged); err != nil {
					cause = errors.Join(cause, fmt.Errorf("cannot withdraw %s; prior output retained at %s: %w", item.target, item.backup, err))
					continue
				}
			}
			if item.movedOld {
				if err := rename(filepath.Join(item.backup, "previous"), item.target); err != nil {
					cause = errors.Join(cause, fmt.Errorf("cannot restore %s; prior output retained at %s: %w", item.target, item.backup, err))
					continue
				}
			}
			if item.backup != "" {
				_ = os.Remove(item.backup)
			}
		}
		return cause
	}
	for i := range items {
		item := &items[i]
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		current, err := os.Lstat(item.target)
		if item.previous == nil {
			if !errors.Is(err, os.ErrNotExist) {
				return rollback(fmt.Errorf("output destination appeared during publication: %s", item.target))
			}
		} else {
			if err != nil || !os.SameFile(item.previous, current) {
				return rollback(fmt.Errorf("output destination changed during publication: %s", item.target))
			}
			item.backup, err = os.MkdirTemp(filepath.Dir(item.target), ".prebuilt-previous-*")
			if err != nil {
				return rollback(err)
			}
			if err := rename(item.target, filepath.Join(item.backup, "previous")); err != nil {
				return rollback(err)
			}
			item.movedOld = true
		}
		if err := rename(item.staged, item.target); err != nil {
			return rollback(fmt.Errorf("publish verified output %s: %w", item.target, err))
		}
		item.published = true
	}
	// Publication has committed. Backup cleanup cannot turn success into a
	// reported failed build after the verified artifacts are already visible.
	for _, item := range items {
		if item.backup != "" {
			_ = os.RemoveAll(item.backup)
		}
	}
	return nil
}
