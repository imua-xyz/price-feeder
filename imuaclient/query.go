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
func (ec imuaClient) GetStakerInfos(assetID string) ([]*oracleTypes.StakerInfo, uint64, error) {
	reqPage := &query.PageRequest{
		Key:        []byte{},
		Offset:     0,
		Limit:      100,
		CountTotal: false,
		Reverse:    false,
	}
	var ret []*oracleTypes.StakerInfo
	var version uint64
	for reqPage.Key != nil {
		stakerInfoRes, err := ec.oracleClient.StakerInfos(context.Background(), &oracleTypes.QueryStakerInfosRequest{AssetId: assetID, Pagination: reqPage})
		if err != nil {
			return []*oracleTypes.StakerInfo{}, 0, fmt.Errorf("failed to get stakerInfos from oracleClient, error:%w", err)
		}
		ret = append(ret, stakerInfoRes.StakerInfos...)
		if version == 0 {
			version = uint64(stakerInfoRes.Version)
		} else if version != uint64(stakerInfoRes.Version) {
			// version has changed during the query
			version = 0
			ret = nil
			reqPage.Key = nil
			continue
		}
		reqPage.Key = stakerInfoRes.Pagination.NextKey
	}
	return ret, version, nil
}

// GetStakerInfos get the stakerInfos corresponding to stakerAddr for the assetID
func (ec imuaClient) GetStakerInfo(assetID, stakerAddr string) ([]*oracleTypes.StakerInfo, int64, error) {
	stakerInfoRes, err := ec.oracleClient.StakerInfos(context.Background(), &oracleTypes.QueryStakerInfosRequest{AssetId: assetID})
	if err != nil {
		return []*oracleTypes.StakerInfo{}, 0, fmt.Errorf("failed to get stakerInfo from oracleClient, error:%w", err)
	}
	return stakerInfoRes.StakerInfos, stakerInfoRes.Version, nil
}
