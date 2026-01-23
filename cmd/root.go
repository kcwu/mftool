package cmd

import (
	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "mftool",
	Short: "mftool is a tool to manipulate MapsForge map file",
	Run: func(cmd *cobra.Command, args []string) {
		// Do Stuff Here
	},
}

var Verbose bool
var CpuProfile string

func init() {
	RootCmd.PersistentFlags().BoolVarP(&Verbose, "verbose", "v", false, "verbose output")
	RootCmd.PersistentFlags().StringVar(&CpuProfile, "cpuprofile", "", "write cpu profile to file")
}
