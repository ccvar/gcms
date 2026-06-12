package version

import (
	"os"
	"runtime"
	"strings"
)

var (
	Version = "dev"
	Commit  = ""
	BuiltAt = ""
	Repo    = "ccvar/gcms-releases"
)

type Info struct {
	Version  string
	Commit   string
	BuiltAt  string
	Repo     string
	GOOS     string
	GOARCH   string
	Platform string
}

func Current() Info {
	return Info{
		Version:  Version,
		Commit:   Commit,
		BuiltAt:  BuiltAt,
		Repo:     releaseRepo(),
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func releaseRepo() string {
	if repo := strings.TrimSpace(os.Getenv("GCMS_RELEASE_REPO")); repo != "" {
		return repo
	}
	return Repo
}

func AssetSuffix() string {
	if runtime.GOOS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}
