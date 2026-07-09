// Package buildinfo exposes the server's version and VCS metadata. Version is
// stamped at link time (-ldflags "-X mcp.chic.md/internal/buildinfo.Version=..."),
// while the git revision and build time are read from the build info the Go
// toolchain embeds automatically when building from a VCS working tree.
package buildinfo

import (
	"log/slog"
	"runtime/debug"
)

// Version is overridden at build time via -ldflags; "dev" for un-stamped builds.
var Version = "dev"

type Info struct {
	Version   string `json:"version"`
	Revision  string `json:"revision,omitempty"`
	Time      string `json:"time,omitempty"`
	Modified  bool   `json:"modified,omitempty"`
	GoVersion string `json:"go_version,omitempty"`
}

// Get returns build metadata. VCS fields are set only when the toolchain
// embedded them (module build with a git tree present).
func Get() Info {
	info := Info{Version: Version}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	info.GoVersion = bi.GoVersion
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = shortRev(s.Value)
		case "vcs.time":
			info.Time = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

func shortRev(rev string) string {
	const short = 12
	if len(rev) > short {
		return rev[:short]
	}
	return rev
}

// LogValue implements slog.LogValuer so an Info logs as a compact attr group.
func (i Info) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("version", i.Version)}
	if i.Revision != "" {
		attrs = append(attrs, slog.String("revision", i.Revision))
	}
	if i.Modified {
		attrs = append(attrs, slog.Bool("modified", true))
	}
	return slog.GroupValue(attrs...)
}
