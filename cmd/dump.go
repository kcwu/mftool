package cmd

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/mapsforge"
)

func init() {
	dumpCmd.Flags().StringVar(&flagDumpTile, "tile", "", "dump only specific tile")
	dumpCmd.Flags().BoolVar(&flagDumpAll, "all", false, "dump all tile")
	dumpCmd.Flags().BoolVar(&flagDumpHeader, "header", false, "dump only header")

	RootCmd.AddCommand(dumpCmd)
}

var dumpCmd = &cobra.Command{
	Use:   "dump [--header | --all | --tile si,x,y] a.map",
	Short: "dump content of map file",
	RunE: func(cmd *cobra.Command, args []string) error {
		return mapsforge.CmdDump(args, flagDumpHeader, flagDumpAll, flagDumpTile)
	},
}

var flagDumpAll bool
var flagDumpHeader bool
var flagDumpTile string
