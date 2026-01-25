package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

func init() {
	mergeCmd.Flags().StringVarP(&flagMergeOutput, "output", "o", "", "output map file (required)")
	mergeCmd.Flags().BoolVarP(&flagMergeForce, "force", "f", false, "overwrite output file if it exists")
	mergeCmd.Flags().StringVar(&flagMergeTile, "tile", "", "merge only specific tile si,x,y")
	mergeCmd.MarkFlagRequired("output")
	RootCmd.AddCommand(mergeCmd)
}

var (
	flagMergeTile   string
	flagMergeOutput string
	flagMergeForce  bool
)

var mergeCmd = &cobra.Command{
	Use:   "merge -o output.map input1.map input2.map ...",
	Short: "merge multiple map files",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		outputPath := flagMergeOutput
		inputPaths := args

		if !flagMergeForce {
			if _, err := os.Stat(outputPath); err == nil {
				return fmt.Errorf("output file %s already exists (use -f to overwrite)", outputPath)
			}
		}

		return mapsforge.MergeMaps(inputPaths, outputPath, flagMergeTile)
	},
}
