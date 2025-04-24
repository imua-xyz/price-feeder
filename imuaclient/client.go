package imuaclient

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	"cosmossdk.io/simapp/params"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/client"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/tx"
	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/gorilla/websocket"
	oracletypes "github.com/imua-xyz/imuachain/x/oracle/types"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"google.golang.org/grpc"
)

var _ ImuaClientInf = &imuaClient{}

// imuaClient implements imuaClientInf interface to serve as a grpc client to interact with eoxored grpc service
type imuaClient struct {
	logger   feedertypes.LoggerInf
	grpcConn *grpc.ClientConn

	// params for sign/send transactions
	privKey cryptotypes.PrivKey
	pubKey  cryptotypes.PubKey
	encCfg  params.EncodingConfig
	txCfg   client.TxConfig
	chainID string

	// client to broadcast transactions to eoxocred
	txClient      tx.ServiceClient
	txClientDebug *rpchttp.HTTP

	// wsclient interact with imuad
	wsClient   *websocket.Conn
	wsEndpoint string
	wsDialer   *websocket.Dialer
	// wsStop channel used to signal ws close
	wsStop chan struct{}
	//	wsStopRet chan struct{}
	//	wsActiveRoutines *atomic.Int32
	wsLock           *sync.Mutex
	wsActiveRoutines *int
	wsActive         *bool
	// wsEventsCh       chan EventRes
	wsEventsCh chan EventInf
	// client to query from imuad
	oracleClient oracletypes.QueryClient
}

// NewImuaClient creates a imua-client used to do queries and send transactions to imuad
func NewImuaClient(logger feedertypes.LoggerInf, endpoint, wsEndpoint, endpointDebug string, privKey cryptotypes.PrivKey, encCfg params.EncodingConfig, chainID string, txOnly bool) (*imuaClient, error) {
	ec := &imuaClient{
		logger:           logger,
		privKey:          privKey,
		pubKey:           privKey.PubKey(),
		encCfg:           encCfg,
		txCfg:            encCfg.TxConfig,
		wsEndpoint:       wsEndpoint,
		wsActiveRoutines: new(int),
		wsActive:         new(bool),
		wsLock:           new(sync.Mutex),
		wsStop:           make(chan struct{}),
		wsEventsCh:       make(chan EventInf),
	}

	var err error
	if txOnly && len(endpointDebug) == 0 {
		return nil, errors.New("rpc endpoint is empty under debug mode")
	}
	if len(endpointDebug) > 0 {
		ec.txClientDebug, err = client.NewClientFromNode(endpointDebug)
		if err != nil {
			return nil, fmt.Errorf("failed to create new client for debug, endponit:%s, error:%v", endpointDebug, err)
		}
	}
	// grpc connection, websocket is not needed for txOnly mode when do debug
	if !txOnly {
		ec.logger.Info("establish grpc connection")
		ec.grpcConn, err = createGrpcConn(endpoint, encCfg)
		if err != nil {
			return nil, feedertypes.ErrInitConnectionFail.Wrap(fmt.Sprintf("failed to create new Imuaclient, endpoint:%s, error:%v", endpoint, err))
		}

		// setup txClient
		ec.txClient = sdktx.NewServiceClient(ec.grpcConn)
		// setup queryClient
		ec.oracleClient = oracletypes.NewQueryClient(ec.grpcConn)
		// setup wsClient
		u, err := url.Parse(wsEndpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to parse wsEndpoint, wsEndpoint:%s, error:%w", wsEndpoint, err)
		}
		ec.wsDialer = &websocket.Dialer{
			NetDial: func(_, _ string) (net.Conn, error) {
				return net.Dial("tcp", u.Host)
			},
			Proxy: http.ProxyFromEnvironment,
		}
	}
	return ec, nil
}

func (ec *imuaClient) Close() {
	ec.CloseWs()
	ec.CloseGRPC()
}

// Close close grpc connection
func (ec *imuaClient) CloseGRPC() {
	ec.grpcConn.Close()
}

func (ec *imuaClient) CloseWs() {
	if ec.wsClient == nil {
		return
	}
	ec.StopWsRoutines()
	ec.wsClient.Close()
}

// GetClient returns defaultImuaClient and a bool value to tell if that defaultImuaClient has been initialized
func GetClient() (*imuaClient, bool) {
	if defaultImuaClient == nil {
		return nil, false
	}
	return defaultImuaClient, true
}
