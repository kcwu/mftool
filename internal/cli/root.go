package cli

import (
	"log"
	"os"
	"runtime/pprof"

	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "mftool",
	Short: "mftool is a tool to manipulate MapsForge map file",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if CpuProfile != "" {
			f, err := os.Create(CpuProfile)
			if err != nil {
				log.Fatal(err)
			}
			pprof.StartCPUProfile(f)
		}
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if CpuProfile != "" {
			pprof.StopCPUProfile()
		}
	},
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var Verbose bool
var CpuProfile string

func init() {
	RootCmd.PersistentFlags().BoolVarP(&Verbose, "verbose", "v", false, "verbose output")
	RootCmd.PersistentFlags().StringVar(&CpuProfile, "cpuprofile", "", "write cpu profile to file")
}
