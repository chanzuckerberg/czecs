package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	debug bool
	quiet bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "czecs",
	Short: "ECS deployment tool",
	Long: `czecs manages deployment of task definitions in Amazon ECS.

czecs takes a task definition template and any user provided values to fill in the template,
creates a corresponding task definition, and modifies/creates the ECS service.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable verbose output")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "do not output to console; use return code to determine success/failure")
}
