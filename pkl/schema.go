// Package schema holds the pkl schema koldstart.pkl files amend, as this koldstart reads it, for
// checking a file without fetching the released package.
package schema

import "embed"

// Files are Project.pkl and kdst.pkl.
//
//go:embed Project.pkl kdst.pkl
var Files embed.FS

// The modules a koldstart.pkl amends and imports, by their file name in Files.
const (
	Project = "Project.pkl"
	Kdst    = "kdst.pkl"
)

// PackageURI is the uri of a module of the schema package released with koldstart version.
func PackageURI(version, module string) string {
	return "package://github.com/kontrolplane/koldstart/releases/download/v" + version + "/koldstart@" + version + "#/" + module
}
