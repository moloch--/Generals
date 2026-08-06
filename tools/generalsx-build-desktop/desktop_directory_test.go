package main

import (
	"strings"
	"testing"
)

func TestParseXDGDesktopDirectory(t *testing.T) {
	t.Parallel()
	home := "/home/commander"
	for _, test := range []struct {
		name     string
		contents string
		want     string
		found    bool
		wantErr  string
	}{
		{
			name: "localized home path", contents: "XDG_DOWNLOAD_DIR=\"$HOME/Downloads\"\nXDG_DESKTOP_DIR=\"$HOME/Schreibtisch\"\n",
			want: "/home/commander/Schreibtisch", found: true,
		},
		{
			name: "braced home path", contents: "XDG_DESKTOP_DIR=\"${HOME}/Desktop Space\"\n",
			want: "/home/commander/Desktop Space", found: true,
		},
		{
			name: "absolute path", contents: "XDG_DESKTOP_DIR=\"/media/shared/Desktop\"\n",
			want: "/media/shared/Desktop", found: true,
		},
		{
			name: "escaped shell literals", contents: "XDG_DESKTOP_DIR=\"/media/Desktop/\\$cash/\\`archive\"\n",
			want: "/media/Desktop/$cash/`archive", found: true,
		},
		{name: "missing key", contents: "XDG_DOWNLOAD_DIR=\"$HOME/Downloads\"\n"},
		{name: "disabled Desktop", contents: "XDG_DESKTOP_DIR=\"$HOME/\"\n", found: true, wantErr: "disabled"},
		{name: "relative path", contents: "XDG_DESKTOP_DIR=\"Desktop\"\n", found: true, wantErr: "must be absolute"},
		{name: "unknown variable", contents: "XDG_DESKTOP_DIR=\"$WORKSPACE/Desktop\"\n", found: true, wantErr: "unsupported variable"},
		{name: "command substitution", contents: "XDG_DESKTOP_DIR=\"$(touch /tmp/unsafe)\"\n", found: true, wantErr: "command substitution"},
		{name: "unquoted value", contents: "XDG_DESKTOP_DIR=$HOME/Desktop\n", found: true, wantErr: "double quoted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, found, err := parseXDGDesktopDirectory(home, []byte(test.contents))
			if found != test.found {
				t.Fatalf("found = %t, want %t", found, test.found)
			}
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Desktop path = %q, want %q", got, test.want)
			}
		})
	}
}
