package cmd

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/mapsforge"
)

func init() {
	mergeCmd.Flags().StringVar(&flagMergeTile, "tile", "", "merge only specific tile si,x,y")
	RootCmd.AddCommand(mergeCmd)
}

var flagMergeTile string

var mergeCmd = &cobra.Command{
	Use:   "merge output.map input1.map input2.map ...",
	Short: "merge multiple map files",
	Args:  cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		outputPath := args[0]
		inputPaths := args[1:]
		return mapsforge.MergeMaps(inputPaths, outputPath, flagMergeTile)
	},
}
