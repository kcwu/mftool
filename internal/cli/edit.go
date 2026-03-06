package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var (
	flagEditOutput    string
	flagEditForce     bool
	flagEditComment   string
	flagEditTimestamp int64
)

var editCmd = &cobra.Command{
	Use:   "edit -o output.map input.map",
	Short: "edit map file header (comment, timestamp)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := args[0]
		output := flagEditOutput

		if output == "" {
			return fmt.Errorf("required flag(s) \"output\" not set")
		}

		if !flagEditForce {
			if _, err := os.Stat(output); err == nil {
				return fmt.Errorf("output file %s already exists (use -f to overwrite)", output)
			}
		}

		var commentPtr *string
		if cmd.Flags().Changed("comment") {
			commentPtr = &flagEditComment
		}

		var timestampPtr *int64
		if cmd.Flags().Changed("timestamp") {
			timestampPtr = &flagEditTimestamp
		}

		if commentPtr == nil && timestampPtr == nil {
			return fmt.Errorf("no changes specified (use --comment or --timestamp)")
		}

		return mapsforge.CmdEdit(input, output, commentPtr, timestampPtr)
	},
}

func init() {
	editCmd.Flags().StringVarP(&flagEditOutput, "output", "o", "", "output map file (required)")
	editCmd.Flags().BoolVarP(&flagEditForce, "force", "f", false, "overwrite output file if it exists")
	editCmd.Flags().StringVar(&flagEditComment, "comment", "", "new comment")
	editCmd.Flags().Int64Var(&flagEditTimestamp, "timestamp", 0, "new timestamp (creation date)")
	editCmd.MarkFlagRequired("output")
	RootCmd.AddCommand(editCmd)
}
