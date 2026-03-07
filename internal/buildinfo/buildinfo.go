package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
)

var (
	Version  = "dev"
	Commit   = "unknown"
	Date     = "unknown"
	RepoSlug = "richardsondx/IronLark"
)

func Summary() string {
	return fmt.Sprintf("%s (%s, %s, %s/%s)", Version, Commit, Date, runtime.GOOS, runtime.GOARCH)
}

func AssetName() (string, error) {
	osName := runtime.GOOS
	archName := runtime.GOARCH
	switch archName {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported architecture %q", archName)
	}
	switch osName {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("unsupported operating system %q", osName)
	}
	return fmt.Sprintf("lark_%s_%s.tar.gz", osName, archName), nil
}

func NormalizeVersion(value string) string {
	return strings.TrimSpace(strings.TrimPrefix(value, "v"))
}
