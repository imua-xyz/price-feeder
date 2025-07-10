package types

import (
	"time"

	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	"github.com/imua-xyz/price-feeder/imuaclient"
	feedertypes "github.com/imua-xyz/price-feeder/types"
)

const PrivFile = "priv_validator_key.json"

type RetryConfig struct {
	MaxAttempts int
	Attempts    int
	Interval    time.Duration
}

// GetOracleParamsWithMaxRetry, get oracle params with max retry
// blocked
func GetOracleParamsWithMaxRetry(maxRetry int, ecClient imuaclient.ImuaClientInf, logger feedertypes.LoggerInf, retry RetryConfig) (oracleP *oracletypes.Params, err error) {
	if maxRetry <= 0 {
		maxRetry = retry.MaxAttempts
	}
	for i := 0; i < maxRetry; i++ {
		oracleP, err = ecClient.GetParams()
		if err == nil {
			return
		}
		logger.Error("Failed to get oracle params, retrying...", "count", i, "max", maxRetry, "error", err)
		time.Sleep(retry.Interval)
	}
	return
}
