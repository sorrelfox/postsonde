package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sorrelfox/postsonde/internal/core/secretstore/keychain"
)

// gtkApplicationID is what g_application_id_is_valid accepts (and the
// D-Bus bus name rules the Linux single-instance lock needs): two or more
// elements of letters, digits, '_' and '-', none starting with a digit.
var gtkApplicationID = regexp.MustCompile(`^[A-Za-z_-][A-Za-z0-9_-]*(\.[A-Za-z_-][A-Za-z0-9_-]*)+$`)

// TestAppIDLinuxOptions: the window reports the app ID as its Wayland
// app_id and X11 WM_CLASS only when the GTK application ID is set and the
// program name is left to follow it (Wails v3.0.0 replaces an invalid ID
// with org.wails.postsonde).
func TestAppIDLinuxOptions(t *testing.T) {
	t.Parallel()

	if !gtkApplicationID.MatchString(appID) || len(appID) > 255 {
		t.Fatalf("appID %q is not a valid GTK application ID", appID)
	}
	o := appOptions(Options{Assets: fstest.MapFS{}}, nil)
	if o.Linux.ApplicationID != appID {
		t.Errorf("Linux.ApplicationID = %q, want %q", o.Linux.ApplicationID, appID)
	}
	if o.Linux.ProgramName != "" {
		t.Errorf("Linux.ProgramName = %q; leave it empty so it follows the application ID", o.Linux.ProgramName)
	}
	if got := InstanceID(""); got != appID {
		t.Errorf("InstanceID(\"\") = %q, want the app ID %q", got, appID)
	}
	if want := appID + ".secrets"; keychain.Service != want {
		t.Errorf("keychain.Service = %q, want %q", keychain.Service, want)
	}
}

// TestAppIDMatchesPackaging keeps the files that name the app ID in step
// with appID: the desktop entry and AppStream metadata the Linux packages
// install, the bundle identifier and the Windows manifest.
func TestAppIDMatchesPackaging(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	want := map[string][]string{
		"build/linux/" + appID + ".desktop": {
			"\nStartupWMClass=" + appID + "\n",
		},
		"build/linux/" + appID + ".metainfo.xml": {
			"<id>" + appID + "</id>",
			`<launchable type="desktop-id">` + appID + ".desktop</launchable>",
		},
		"build/linux/nfpm/postsonde.yaml": {
			"dst: /usr/share/applications/" + appID + ".desktop\n",
			"dst: /usr/share/metainfo/" + appID + ".metainfo.xml\n",
		},
		"build/linux/nfpm/postsonde-gtk3.yaml": {
			"dst: /usr/share/applications/" + appID + ".desktop\n",
			"dst: /usr/share/metainfo/" + appID + ".metainfo.xml\n",
		},
		"build/config.yml": {
			`productIdentifier: "` + appID + `"`,
		},
		"build/darwin/Info.plist": {
			"<key>CFBundleIdentifier</key>\n\t\t<string>" + appID + "</string>",
		},
		"build/darwin/Info.dev.plist": {
			"<key>CFBundleIdentifier</key>\n\t\t<string>" + appID + "</string>",
		},
		"build/windows/wails.exe.manifest": {
			`name="` + appID + `"`,
		},
	}
	for file, snippets := range want {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			t.Error(err)
			continue
		}
		// A Windows checkout may have CRLF line ends.
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		for _, s := range snippets {
			if !strings.Contains(text, s) {
				t.Errorf("%s does not contain %q", file, s)
			}
		}
	}
}
