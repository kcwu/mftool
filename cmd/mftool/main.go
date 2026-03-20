package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/kcwu/mftool/internal/cli"
)

// archVariantKey returns the GOENV variable name that controls the
// microarchitecture level for the current GOARCH (e.g. "GOAMD64" on amd64).
func archVariantKey() string {
	switch runtime.GOARCH {
	case "amd64":
		return "GOAMD64"
	case "arm":
		return "GOARM"
	case "arm64":
		return "GOARM64"
	case "mips", "mipsle":
		return "GOMIPS"
	case "mips64", "mips64le":
		return "GOMIPS64"
	case "ppc64", "ppc64le":
		return "GOPPC64"
	case "riscv64":
		return "GORISCV64"
	}
	return ""
}

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var commit, archVariant string
	dirty := false
	cgoEnabled := "unknown"
	trimpath, race := false, false
	variantKey := archVariantKey()
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		case "CGO_ENABLED":
			if s.Value == "1" {
				cgoEnabled = "cgo"
			} else {
				cgoEnabled = "nocgo"
			}
		case "-trimpath":
			trimpath = s.Value == "true"
		case "-race":
			race = s.Value == "true"
		default:
			if s.Key == variantKey {
				archVariant = s.Value
			}
		}
	}
	if commit == "" {
		commit = "dev"
	} else {
		if len(commit) > 16 {
			commit = commit[:16]
		}
		if dirty {
			commit += "-dirty"
		}
	}
	arch := runtime.GOARCH
	if archVariant != "" {
		arch += "/" + archVariant
	}
	v := fmt.Sprintf("%s, %s (%s, %s/%s, %s", Version, commit, info.GoVersion, runtime.GOOS, arch, cgoEnabled)
	if trimpath {
		v += ", trimpath"
	}
	if race {
		v += ", race"
	}
	return v + ")"
}

func main() {
	cli.RootCmd.Version = buildVersion()

	if err := cli.RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
