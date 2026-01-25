package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kcwu/mftool/internal/mapsforge"
)

var validateCmd = &cobra.Command{
	Use:   "validate <file.map>",
	Short: "validate map file integrity",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		file := args[0]
		fmt.Printf("Validating %s ...\n", file)
		p, err := mapsforge.ParseFile(file, true)
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
		defer p.Close()
		fmt.Println("File is valid.")
	},
}

func init() {
	RootCmd.AddCommand(validateCmd)
}
