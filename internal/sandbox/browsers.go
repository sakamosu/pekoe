package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
)

// BrowserProfiles are the profile directories of the common browsers on
// this OS, for $HOME and $XDG_CONFIG_HOME now. The local GUI keeps its
// session token in the page's sessionStorage (webui/lib/api.js), which a
// browser may write into its profile to restore the tab (Chrome: …/Session
// Storage). A command that read it, and the GUI's 127.0.0.1:<port>, would
// act as the person (docs/gui.md, the threat model). The cookies of every
// site are there too.
//
// Paths that do not exist are listed all the same: Policy.DenyReadIfPresent
// leaves them alone on Linux. The list cannot be complete (a browser not
// named here, a profile moved with --user-data-dir).
func BrowserProfiles() []string {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return nil
	}
	return browserProfiles(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"))
}

// macBrowsers are below ~/Library.
var macBrowsers = []string{
	"Application Support/Google/Chrome",
	"Application Support/Google/Chrome Beta",
	"Application Support/Google/Chrome Dev",
	"Application Support/Google/Chrome Canary",
	"Application Support/Chromium",
	"Application Support/Microsoft Edge",
	"Application Support/Microsoft Edge Beta",
	"Application Support/Microsoft Edge Dev",
	"Application Support/Microsoft Edge Canary",
	"Application Support/BraveSoftware",
	"Application Support/Arc",
	"Application Support/Vivaldi",
	"Application Support/com.operasoftware.Opera",
	"Application Support/Firefox",
	"Safari",
	"Containers/com.apple.Safari",
	"WebKit/com.apple.Safari",
}

// linuxConfigBrowsers are below ~/.config (and $XDG_CONFIG_HOME).
var linuxConfigBrowsers = []string{
	"google-chrome", "google-chrome-beta", "google-chrome-unstable",
	"chromium",
	"microsoft-edge", "microsoft-edge-beta", "microsoft-edge-dev",
	"BraveSoftware", "vivaldi", "opera",
}

// linuxHomeBrowsers are below ~: Firefox, and the snap and Flatpak
// packages, which keep their profiles in their own directories.
var linuxHomeBrowsers = []string{
	".mozilla",
	"snap/firefox", "snap/chromium",
	".var/app/org.mozilla.firefox", ".var/app/org.chromium.Chromium", ".var/app/com.google.Chrome",
	".var/app/com.brave.Browser", ".var/app/com.microsoft.Edge",
}

func browserProfiles(goos, home, xdgConfig string) []string {
	var out []string
	switch goos {
	case "darwin":
		for _, rel := range macBrowsers {
			out = append(out, filepath.Join(home, "Library", rel))
		}
	case "linux":
		configs := []string{filepath.Join(home, ".config")}
		if filepath.IsAbs(xdgConfig) && filepath.Clean(xdgConfig) != configs[0] {
			configs = append(configs, filepath.Clean(xdgConfig))
		}
		for _, c := range configs {
			for _, rel := range linuxConfigBrowsers {
				out = append(out, filepath.Join(c, rel))
			}
		}
		for _, rel := range linuxHomeBrowsers {
			out = append(out, filepath.Join(home, rel))
		}
	}
	return out
}
