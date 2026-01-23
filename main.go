package main

import (
	"fmt"
	"log"
	"os"
	"runtime/pprof"

	"github.com/kcwu/mftool/cmd"
)

func main() {
	if cmd.CpuProfile != "" {
		f, err := os.Create(cmd.CpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	if err := cmd.RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
