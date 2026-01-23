package cmd

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/mapsforge"
)

var parseCmd = &cobra.Command{
	Use: "parse",
	RunE: func(cmd *cobra.Command, args []string) error {
		return mapsforge.CmdParse(args)
	},
}

func init() {
	RootCmd.AddCommand(parseCmd)
}
