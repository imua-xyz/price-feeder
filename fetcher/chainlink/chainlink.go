package chainlink

import (
	"errors"
	"fmt"
	"strings"
	"time"

	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
)

func (s *source) fetch(token string) (*fetchertypes.PriceInfo, error) {
	if !strings.HasSuffix(token, fetchertypes.BaseCurrency) {
		token += fetchertypes.BaseCurrency
	}
	chainlinkPriceFeedProxy, ok := s.chainlinkProxy.get(token)
	if !ok {
		return nil, feedertypes.ErrSourceTokenNotConfigured.Wrap(fmt.Sprintf("chainlinkProxy not configured for token: %s", token))
	}

	roundData, err := chainlinkPriceFeedProxy.LatestRoundData(&bind.CallOpts{})
	if err != nil {
		return nil, fmt.Errorf("failed to get LatestRoundData of token:%s from chainlink, error:%w", token, err)
	}

	decimals, err := chainlinkPriceFeedProxy.Decimals(&bind.CallOpts{})
	if err != nil {
		return nil, fmt.Errorf("failed to get decimals, error:%w", err)
	}

	return &fetchertypes.PriceInfo{
		Price:     roundData.Answer.String(),
		Decimal:   int32(decimals),
		Timestamp: time.Now().UTC().Format(feedertypes.TimeLayout),
		RoundID:   roundData.RoundId.String(),
	}, nil
}

func (s *source) reload(cfgPath string, token string) error {
	cfg, err := parseConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("failed to parse config file, error:%w", err)
	}
	// add new network from config file
	for network, url := range cfg.URLs {
		network = strings.ToLower(network)
		if err := s.chainlinkProxy.addClient(network, url); err != nil {
			return fmt.Errorf("failed to add ethClient for network:%s with url:%s, error:%w", network, url, err)
		}
	}
	// add proxy for new token matches the required token if found
	for tName, tContract := range cfg.Tokens {
		tName = strings.ToLower(tName)
		if strings.EqualFold(tName, token) {
			if err := s.chainlinkProxy.addToken(map[string]string{tName: tContract}); err != nil {
				s.logger.Error("failed to add proxy when do reload", "source", s.GetName(), "token", tName, "error", err)
			}
			return nil
		}
	}
	return errors.New("token not found in reloaded config file")
}
