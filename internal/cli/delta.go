package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var (
	flagDeltaOutput   string
	flagDeltaForce    bool
	flagDeltaSemantic bool
)

func init() {
	deltaCmd.Flags().StringVarP(&flagDeltaOutput, "output", "o", "", "output delta file (required)")
	deltaCmd.Flags().BoolVarP(&flagDeltaForce, "force", "f", false, "overwrite output file if it exists")
	deltaCmd.Flags().BoolVar(&flagDeltaSemantic, "semantic", false, "print semantic hash of the final map")
	deltaCmd.MarkFlagRequired("output")
	RootCmd.AddCommand(deltaCmd)
}

var deltaCmd = &cobra.Command{
	Use:   "delta -o output.mfd old.map new.map",
	Short: "generate a binary delta between two map files",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !flagDeltaForce {
			if _, err := os.Stat(flagDeltaOutput); err == nil {
				return fmt.Errorf("output file %s already exists (use -f to overwrite)", flagDeltaOutput)
			}
		}
		return mapsforge.CmdDelta(args[0], args[1], flagDeltaOutput, flagDeltaForce, flagDeltaSemantic)
	},
}
