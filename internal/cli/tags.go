package cli

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var tagsCmd = &cobra.Command{
	Use:   "tags <file.map> [file.map ...]",
	Short: "Show statistics of tag usage",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mapsforge.CmdTags(args)
	},
}

func init() {
	RootCmd.AddCommand(tagsCmd)
}
