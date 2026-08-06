package main

import "testing"

func TestExtendedWindowsPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "drive path", path: `C:\Users\Commander\Desktop\GeneralsXZH-sfx.exe`, want: `\\?\C:\Users\Commander\Desktop\GeneralsXZH-sfx.exe`},
		{name: "drive path with forward slashes", path: `c:/Users/Commander/Desktop/GeneralsXZH-sfx.exe`, want: `\\?\c:\Users\Commander\Desktop\GeneralsXZH-sfx.exe`},
		{name: "UNC path", path: `\\server\share\Desktop\GeneralsXZH-sfx.exe`, want: `\\?\UNC\server\share\Desktop\GeneralsXZH-sfx.exe`},
		{name: "UNC path with forward slashes", path: `//server/share/Desktop/GeneralsXZH-sfx.exe`, want: `\\?\UNC\server\share\Desktop\GeneralsXZH-sfx.exe`},
		{name: "extended drive path", path: `\\?\C:\Desktop\GeneralsXZH-sfx.exe`, want: `\\?\C:\Desktop\GeneralsXZH-sfx.exe`},
		{name: "extended UNC path", path: `\\?\UNC\server\share\GeneralsXZH-sfx.exe`, want: `\\?\UNC\server\share\GeneralsXZH-sfx.exe`},
		{name: "device path", path: `\\.\PhysicalDrive0`, want: `\\.\PhysicalDrive0`},
		{name: "relative path", path: `Desktop\GeneralsXZH-sfx.exe`, want: `Desktop\GeneralsXZH-sfx.exe`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := extendedWindowsPath(test.path); got != test.want {
				t.Fatalf("extendedWindowsPath(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}
