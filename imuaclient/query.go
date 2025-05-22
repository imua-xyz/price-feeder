package imuaclient

import (
	"context"
	"fmt"

	query "github.com/cosmos/cosmos-sdk/types/query"
	oracleTypes "github.com/imua-xyz/imuachain/x/oracle/types"
)

// GetParams queries oracle params
func (ec imuaClient) GetParams() (*oracleTypes.Params, error) {
	paramsRes, err := ec.oracleClient.Params(context.Background(), &oracleTypes.QueryParamsRequest{})
	if err != nil {
		return &oracleTypes.Params{}, fmt.Errorf("failed to query oracle params from oracleClient, error:%w", err)
	}
	return &paramsRes.Params, nil

}

// GetLatestPrice returns latest price of specific token
func (ec imuaClient) GetLatestPrice(tokenID uint64) (oracleTypes.PriceTimeRound, error) {
	priceRes, err := ec.oracleClient.LatestPrice(context.Background(), &oracleTypes.QueryGetLatestPriceRequest{TokenId: tokenID})
	if err != nil {
		return oracleTypes.PriceTimeRound{}, fmt.Errorf("failed to get latest price from oracleClient, error:%w", err)
	}
	return priceRes.Price, nil

}

// GetStakerInfos get all stakerInfos for the assetID
func (ec imuaClient) GetStakerInfos(assetID string) ([]*oracleTypes.StakerInfo, *oracleTypes.NSTVersion, error) {
	reqPage := &query.PageRequest{
		Key:        []byte{},
		Offset:     0,
		Limit:      100,
		CountTotal: false,
		Reverse:    false,
	}
	var ret []*oracleTypes.StakerInfo
	var version *oracleTypes.NSTVersion
	for reqPage.Key != nil {
		stakerInfoRes, err := ec.oracleClient.StakerInfos(context.Background(), &oracleTypes.QueryStakerInfosRequest{AssetId: assetID, Pagination: reqPage})
		if err != nil {
			return []*oracleTypes.StakerInfo{}, nil, fmt.Errorf("failed to get stakerInfos from oracleClient, error:%w", err)
		}
		ret = append(ret, stakerInfoRes.StakerInfos...)
		if version == nil {
			version = stakerInfoRes.Version
		} else if version.Version.Version != stakerInfoRes.Version.Version.Version {
			// version has changed during the query
			version = nil
			ret = nil
			reqPage.Key = nil
			continue
		}
		reqPage.Key = stakerInfoRes.Pagination.NextKey
	}
	return ret, version, nil
}
