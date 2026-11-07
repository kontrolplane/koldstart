// Package version is what koldstart can say about itself.
package version

import (
	"runtime"
	"runtime/debug"
)

// Stamped by the linker; see the Makefile, the Dockerfile and .goreleaser.yaml.
//
//	go build -ldflags "-X github.com/kontrolplane/koldstart/internal/version.buildVersion=v0.1.0 ..."
var (
	buildVersion string
	buildCommit  string
	buildDate    string
)

// Info describes one build. `koldstart version -o json` prints it.
type Info struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Modified bool   `json:"modified"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

// Current returns the linker stamps, filling what they left empty from the embedded build info,
// so a plain go build or go install still reports a version, commit and date.
func Current() Info {
	info := Info{
		Version:  buildVersion,
		Commit:   buildCommit,
		Date:     buildDate,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}

	if build, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" && build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Version = build.Main.Version
		}
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = setting.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = setting.Value
				}
			case "vcs.modified":
				info.Modified = info.Modified || setting.Value == "true"
			}
		}
	}

	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.Date == "" {
		info.Date = "unknown"
	}
	return info
}

// String is the line `koldstart version` prints.
func (i Info) String() string {
	commit := i.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if i.Modified {
		commit += "-dirty"
	}
	return i.Version + " (" + commit + ", " + i.Date + ", " + i.Go + " " + i.Platform + ")"
}
