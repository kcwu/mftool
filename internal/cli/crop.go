package cli

import (
	"fmt"
	"math"
	"os"

	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var (
	flagBBox       string
	flagCenter     string
	flagDistance   float64
	flagCropOutput string
	flagCropForce  bool
)

var cropCmd = &cobra.Command{
	Use:   "crop -o output.map input.map [--bbox ...] | [--center ... --distance ...]",
	Short: "crop a map file to a bounding box",
	Args:  cobra.ExactArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if flagBBox == "" && (flagCenter == "" || flagDistance == 0) {
			return fmt.Errorf("either --bbox or --center and --distance must be specified")
		}
		if flagBBox != "" && (flagCenter != "" || flagDistance != 0) {
			return fmt.Errorf("cannot specify both --bbox and --center/--distance")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		output := flagCropOutput
		input := args[0]

		if !flagCropForce {
			if _, err := os.Stat(output); err == nil {
				return fmt.Errorf("output file %s already exists (use -f to overwrite)", output)
			}
		}

		bbox := flagBBox
		if flagCenter != "" {
			var lat, lon float64
			_, err := fmt.Sscanf(flagCenter, "%f,%f", &lat, &lon)
			if err != nil {
				return fmt.Errorf("invalid center format (lat,lon): %v", err)
			}
			// 1 degree latitude ~= 111.32 km
			deltaLat := flagDistance / 111.32
			// 1 degree longitude ~= 111.32 * cos(lat) km
			deltaLon := flagDistance / (111.32 * math.Cos(lat*math.Pi/180))

			minLat := lat - deltaLat
			maxLat := lat + deltaLat
			minLon := lon - deltaLon
			maxLon := lon + deltaLon

			if minLat < -90 || maxLat > 90 {
				fmt.Println("Warning: calculated latitude exceeds valid range [-90, 90]")
			}
			if minLon < -180 || maxLon > 180 {
				fmt.Println("Warning: calculated longitude exceeds valid range [-180, 180]")
			}

			bbox = fmt.Sprintf("%f,%f,%f,%f", minLon, minLat, maxLon, maxLat)
		}

		return mapsforge.CropMap(input, output, bbox)
	},
}

func init() {
	cropCmd.Flags().StringVar(&flagBBox, "bbox", "", "bounding box: minLon,minLat,maxLon,maxLat")
	cropCmd.Flags().StringVar(&flagCenter, "center", "", "center point: lat,lon")
	cropCmd.Flags().Float64Var(&flagDistance, "distance", 0, "distance in km from center")
	cropCmd.Flags().StringVarP(&flagCropOutput, "output", "o", "", "output map file (required)")
	cropCmd.Flags().BoolVarP(&flagCropForce, "force", "f", false, "overwrite output file if it exists")
	cropCmd.MarkFlagRequired("output")
	RootCmd.AddCommand(cropCmd)
}
