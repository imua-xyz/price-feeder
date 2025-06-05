package external

import (
	"github.com/imua-xyz/price-feeder/internal/feeder"
	itypes "github.com/imua-xyz/price-feeder/internal/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

// LoadConf set conf from invoker instead of rootCmd
func StartPriceFeeder(cfgFile, mnemonic, sourcesPath, logPath string, logger feedertypes.LoggerInf) bool {
	if len(cfgFile) == 0 {
		return false
	}
	conf, err := feedertypes.InitConfig(cfgFile)
	if err != nil {
		logger.Error("Error loading config file: %s", err)
		return false
	}
	if len(logPath) > 0 {
		conf.Log.Path = logPath
	}

	if len(conf.Log.Path) > 0 {
		// replace logger with the one from config when redirecting logs to file
		logger = feedertypes.NewLogger(conf.Log)
	}
	// Start price feeder
	feeder.RunPriceFeeder(conf, logger, mnemonic, sourcesPath, itypes.PrivFile, false)

	return true
}
