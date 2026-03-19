package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var (
	flagApplyOutput   string
	flagApplyForce    bool
	flagApplySemantic bool
)

func init() {
	applyCmd.Flags().StringVarP(&flagApplyOutput, "output", "o", "", "output map file (required)")
	applyCmd.Flags().BoolVarP(&flagApplyForce, "force", "f", false, "overwrite output file if it exists")
	applyCmd.Flags().BoolVar(&flagApplySemantic, "semantic", false, "print semantic hash of the output map")
	applyCmd.MarkFlagRequired("output")
	RootCmd.AddCommand(applyCmd)
}

var applyCmd = &cobra.Command{
	Use:   "apply -o output.map base.map delta1.mfd [delta2.mfd ...]",
	Short: "apply one or more delta files to a base map",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !flagApplyForce {
			if _, err := os.Stat(flagApplyOutput); err == nil {
				return fmt.Errorf("output file %s already exists (use -f to overwrite)", flagApplyOutput)
			}
		}
		basePath := args[0]
		deltaFiles := args[1:]
		return mapsforge.CmdApply(basePath, deltaFiles, flagApplyOutput, flagApplyForce, flagApplySemantic)
	},
}
