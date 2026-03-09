package cli

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var diffCmd = &cobra.Command{
	Use:   "diff a.map b.map",
	Short: "compare two map files",
	RunE: func(cmd *cobra.Command, args []string) error {
		return mapsforge.CmdDiff(args, flagDetail, flagIgnoreComment, flagIgnoreTimestamp, flagStrict)
	},
}

var flagDetail bool
var flagIgnoreComment bool
var flagIgnoreTimestamp bool
var flagStrict bool

func init() {
	diffCmd.Flags().BoolVarP(&flagDetail, "verbose", "v", false, "show detail of diff")
	diffCmd.Flags().BoolVar(&flagIgnoreComment, "ignore-comment", false, "ignore comment mismatch")
	diffCmd.Flags().BoolVar(&flagIgnoreTimestamp, "ignore-timestamp", false, "ignore creation date mismatch")
	diffCmd.Flags().BoolVarP(&flagStrict, "strict", "s", false, "report tag ordering mismatch between files")
	RootCmd.AddCommand(diffCmd)
}
