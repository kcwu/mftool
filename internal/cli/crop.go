package cli

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var flagBBox string

var cropCmd = &cobra.Command{
	Use:   "crop output.map input.map --bbox minLon,minLat,maxLon,maxLat",
	Short: "crop a map file to a bounding box",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		output := args[0]
		input := args[1]
		return mapsforge.CropMap(input, output, flagBBox)
	},
}

func init() {
	cropCmd.Flags().StringVar(&flagBBox, "bbox", "", "bounding box: minLon,minLat,maxLon,maxLat")
	cropCmd.MarkFlagRequired("bbox")
	RootCmd.AddCommand(cropCmd)
}
