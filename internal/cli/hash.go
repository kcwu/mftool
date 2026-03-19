package cli

import (
	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var hashCmd = &cobra.Command{
	Use:   "hash a.map",
	Short: "compute semantic hash of a map file",
	Long: `Compute a 64-character hex semantic hash of a map file.

Two maps have identical hashes if and only if they are semantically equal
(i.e. "diff" would report no differences). The hash is independent of
internal tag ID numbering and element ordering within each tile.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return mapsforge.CmdHash(args, hashIgnoreComment, hashIgnoreTimestamp)
	},
}

var hashIgnoreComment bool
var hashIgnoreTimestamp bool

func init() {
	hashCmd.Flags().BoolVar(&hashIgnoreComment, "ignore-comment", false, "exclude comment from hash")
	hashCmd.Flags().BoolVar(&hashIgnoreTimestamp, "ignore-timestamp", false, "exclude creation date from hash")
	RootCmd.AddCommand(hashCmd)
}
