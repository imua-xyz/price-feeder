/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"os"

	feedertypes "github.com/imua-xyz/price-feeder/types"
	"github.com/spf13/cobra"
)

var (
	cfgFile       string
	sourcesPath   string
	feederConfig  *feedertypes.Config
	logImuaFormat bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "price-feeder",
	Short: "A brief description of your application",
	Long: `A longer description that spans multiple lines and likely contains
examples and usage of using your application. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	// Uncomment the following line if your bare application
	// has an action associated with it:
	// Run: func(cmd *cobra.Command, args []string) { },
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Name() == "version" || cmd.Name() == "status" {
			return nil
		}
		// load and parse config file
		var err error
		feederConfig, err = feedertypes.InitConfig(cfgFile)
		if len(logPath) > 0 {
			// set log file
			feederConfig.Log.Path = logPath
		}
		return err
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	//	cobra.OnInitialize(initConfig)

	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.price-feeder.yaml)")
	rootCmd.PersistentFlags().StringVar(&sourcesPath, "sources_path", "", "config file of sources(default is $HOME/.xx.yaml)")
	rootCmd.PersistentFlags().BoolVar(&logImuaFormat, "log_imua_format", false, "use imua log format")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.

	startCmd.Flags().StringVarP(&mnemonic, "mnemonic", "m", "", "mnemonic of consensus key")
	startCmd.Flags().StringVarP(&logPath, "log_path", "l", "", "log file name")
	startCmd.Flags().IntVar(&grpcPort, "status_port", 0, "gRPC port to listen on for status server")

	statusCmd.Flags().StringVarP(&grpcAddr, "grpc_addr", "g", "", "gRPC address to connect to the price feeder")

	rootCmd.AddCommand(
		startCmd,
		debugStartCmd,
		versionCmd,
		statusCmd,
	)
}
