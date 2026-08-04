package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestUnixProfilesExcludeRawSteamWindowsRuntime(t *testing.T) {
	t.Parallel()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	profileRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", "profiles"))
	windowsOnly := []string{
		"BINKW32.DLL",
		"binkw32.dll",
		"mss32.dll",
		"MSS32.DLL",
		"P2XDLL.DLL",
		"d3d8.dll",
		"DebugWindow.dll",
		"ParticleEditor.dll",
		"patchw32.dll",
		"steam_api.dll",
		"dbghelp.dll.bak",
		"Generals.exe",
		"GenToolUpdater.exe",
		"WorldBuilder.exe",
		"launcher/Generals.exe",
		"Game.dat",
		"MSS/mssmp3.asi",
		"ZH_Generals/MSS/mssvoice.asi",
	}
	for _, profileName := range []string{"macos-zh.exclude", "linux-zh.exclude"} {
		profileName := profileName
		t.Run(profileName, func(t *testing.T) {
			t.Parallel()
			exclude, err := parseExclusionProfile(filepath.Join(profileRoot, profileName))
			if err != nil {
				t.Fatal(err)
			}
			for _, relative := range windowsOnly {
				matched, err := exclude(relative, nil)
				if err != nil {
					t.Fatalf("exclude %q: %v", relative, err)
				}
				if !matched {
					t.Errorf("profile did not exclude raw Windows runtime %q", relative)
				}
			}
		})
	}
}

func TestWindowsProfileRetainsOwnedRuntimeDLLs(t *testing.T) {
	t.Parallel()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	profile := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", "profiles", "windows-zh.exclude"))
	exclude, err := parseExclusionProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"BINKW32.DLL", "mss32.dll"} {
		matched, err := exclude(relative, nil)
		if err != nil {
			t.Fatal(err)
		}
		if matched {
			t.Errorf("Windows profile excluded required owned DLL %q", relative)
		}
	}
	for _, relative := range []string{
		"d3d8.dll",
		"P2XDLL.DLL",
		"Game.dat",
		"Generals.exe",
		"launcher/Generals.exe",
	} {
		matched, err := exclude(relative, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !matched {
			t.Errorf("Windows profile retained unvalidated raw runtime %q", relative)
		}
	}
}
