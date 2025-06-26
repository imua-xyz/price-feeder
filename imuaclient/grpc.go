package imuaclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"cosmossdk.io/simapp/params"
	"github.com/cosmos/cosmos-sdk/codec"
	feedertypes "github.com/imua-xyz/price-feeder/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

type connManager struct {
	mu     sync.Mutex
	conn   *grpc.ClientConn
	target string
	opts   []grpc.DialOption
	logger feedertypes.LoggerInf
}

// NewConnManager creates a reusable connection manager
func newConnManager(target string, logger feedertypes.LoggerInf, opts ...grpc.DialOption) *connManager {
	return &connManager{
		target: target,
		opts:   opts,
		logger: logger,
	}
}

// Get returns a healthy connection, reconnects if needed
func (cm *connManager) get(ctx context.Context) (*grpc.ClientConn, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Check if connection is valid
	if cm.conn == nil || cm.conn.GetState() == connectivity.Shutdown {
		logger.Info("grpc: connection is nil or shutdown, creating new connection", "target", cm.target)
		if cm.conn != nil {
			_ = cm.conn.Close()
		}
		ctx, cancel := withDefaultTimeout(ctx)
		if cancel != nil {
			defer cancel()
		}
		conn, err := grpc.DialContext(ctx, cm.target, cm.opts...)
		if err != nil {
			return nil, err
		}
		cm.conn = conn
	} else if cm.conn.GetState() == connectivity.TransientFailure {
		logger.Info("grpc: connection in transient failure state, attempting to reconnect", "target", cm.target)
		_ = cm.conn.Close()

		ctx, cancel := withDefaultTimeout(ctx)
		if cancel != nil {
			defer cancel()
		}
		conn, err := grpc.DialContext(ctx, cm.target, cm.opts...)
		if err != nil {
			return nil, err
		}
		cm.conn = conn
	}
	return cm.conn, nil
}

// Close shuts down the current connection
func (cm *connManager) closeConn() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.conn != nil {
		return cm.conn.Close()
	}
	return nil
}

func initGRPCManager(target string, encCfg params.EncodingConfig, logger feedertypes.LoggerInf) (*connManager, error) {
	if target == "" {
		return nil, fmt.Errorf("target cannot be empty")
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(codec.NewProtoCodec(encCfg.InterfaceRegistry).GRPCCodec())),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                150 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: false,
		}),
		grpc.WithBlock(),
	}

	connManager := newConnManager(target, logger, opts...)
	return connManager, nil
}

func withDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, nil
	}
	return context.WithTimeout(ctx, 10*time.Second)
}
