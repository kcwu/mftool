package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var (
	flagLoadOutput string
	flagLoadForce  bool
)

var loadCmd = &cobra.Command{
	Use:   "load -o output.map input.toml",
	Short: "build a map file from TOML dump",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		output := flagLoadOutput
		input := args[0]

		if output == "" {
			return fmt.Errorf("required flag(s) \"output\" not set")
		}

		if !flagLoadForce {
			if _, err := os.Stat(output); err == nil {
				return fmt.Errorf("output file %s already exists (use -f to overwrite)", output)
			}
		}

		return mapsforge.LoadMapFromTOML(input, output)
	},
}

func init() {
	loadCmd.Flags().StringVarP(&flagLoadOutput, "output", "o", "", "output map file (required)")
	loadCmd.Flags().BoolVarP(&flagLoadForce, "force", "f", false, "overwrite output file if it exists")
	loadCmd.MarkFlagRequired("output")
	RootCmd.AddCommand(loadCmd)
}
