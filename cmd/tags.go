package cmd

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/mapsforge"
)

var tagsCmd = &cobra.Command{
	Use:   "tags",
	Short: "Show statistics of tag usage",
	RunE: func(cmd *cobra.Command, args []string) error {
		return mapsforge.CmdTags(args)
	},
}

func init() {
	RootCmd.AddCommand(tagsCmd)
}
