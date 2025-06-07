package types

import fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"

func (p *PriceMsg) GetPriceInfo() fetchertypes.PriceInfo {
	return fetchertypes.PriceInfo{
		Price:     p.Price,
		Decimal:   p.Decimal,
		RoundID:   p.DetId,
		Timestamp: p.Timestamp,
	}
}
