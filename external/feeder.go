package external

import (
	"fmt"

	"github.com/imua-xyz/price-feeder/internal/feeder"
	"github.com/imua-xyz/price-feeder/internal/status"
	istatus "github.com/imua-xyz/price-feeder/internal/status"
	istatustypes "github.com/imua-xyz/price-feeder/internal/status/types"
	itypes "github.com/imua-xyz/price-feeder/internal/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

// LoadConf set conf from invoker instead of rootCmd
func StartPriceFeeder(cfgFile, mnemonic, sourcesPath, logPath string, statusPort int, logger feedertypes.LoggerInf) error {
	if len(cfgFile) == 0 {
		return fmt.Errorf("config file path is required")
	}
	conf, err := feedertypes.InitConfig(cfgFile)
	if err != nil {
		logger.Error("Error loading config file: %s", err)
		return err
	}
	if len(logPath) > 0 {
		conf.Log.Path = logPath
	}

	if len(conf.Log.Path) > 0 {
		// replace logger with the one from config when redirecting logs to file
		logger = feedertypes.NewLogger(conf.Log)
	}
	// Start price feeder
	exitedCh, f := feeder.RunPriceFeeder(conf, logger, mnemonic, sourcesPath, itypes.PrivFile, false)
	if statusPort <= 0 {
		statusPort = conf.Status.Grpc
	}
	errCh := status.StartStatusServer(f, statusPort)
	select {
	case err := <-errCh:
		logger.Error("status server error", "error", err)
		return err
	case <-exitedCh:
		return nil
	}
}

func GetAllTokens(grpcAddr string) (*istatustypes.GetAllTokensResponse, error) {
	return istatus.GetAllTokens(grpcAddr)
}

func FilterErrors(err error) (string, error) {
	return istatus.FilterErrors(err)
}
