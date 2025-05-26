/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"os"

	"cosmossdk.io/log"
	tmlog "github.com/cometbft/cometbft/libs/log"
	serverlog "github.com/cosmos/cosmos-sdk/server/log"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

const privFile = "priv_validator_key.json"

var mnemonic string
var logPath string

// startCmd represents the start command
var startCmd = &cobra.Command{
	Use:   "start",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var logger feedertypes.LoggerInf
		if logImuaFormat {
			// use imua format logger
			logger = ImuaFormatLogger(feederConfig.Log)
		} else {
			logger = feedertypes.NewLogger(feederConfig.Log)
		}
		RunPriceFeeder(feederConfig, logger, mnemonic, sourcesPath, true)
		return nil
	},
}

func ImuaFormatLogger(lc feedertypes.LogConf) feedertypes.LoggerInf {
	var opts []log.Option
	logLvl, err := zerolog.ParseLevel(feedertypes.DefaultIfZero(lc.Level, feedertypes.DefaultLogLevel))
	if err != nil {
		logLvl = zerolog.InfoLevel
	}
	opts = append(opts, log.LevelOption(logLvl))
	logger := log.NewLogger(tmlog.NewSyncWriter(os.Stdout), opts...).With(log.ModuleKey, "price-feeder")
	return serverlog.CometLoggerWrapper{Logger: logger}
}
