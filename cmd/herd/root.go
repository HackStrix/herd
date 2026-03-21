package main

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "herd",
	Short: "herd is a blazing fast sidecar daemon for process pooling",
	Long:  `herd transforms passive Go libraries into a production-grade cross-language daemon built for high-performance process pooling.`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}
