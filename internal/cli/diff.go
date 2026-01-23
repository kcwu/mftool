package cli

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var diffCmd = &cobra.Command{
	Use:   "diff a.map b.map",
	Short: "compare two map files",
	RunE: func(cmd *cobra.Command, args []string) error {
		return mapsforge.CmdDiff(args, flagDetail)
	},
}

var flagDetail bool

func init() {
	diffCmd.Flags().BoolVar(&flagDetail, "detail", false, "show detail of diff")
	RootCmd.AddCommand(diffCmd)
}
