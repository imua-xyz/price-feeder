package status

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"

	fetchertypes "github.com/imua-xyz/price-feeder/fetcher/types"
	types "github.com/imua-xyz/price-feeder/internal/status/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	grpcstatus "google.golang.org/grpc/status"
)

const nstPrefix = "nst"

type fetcherInf interface {
	GetTokensStatus() map[string]map[string]*fetchertypes.TokenStatus
}

type StatusServer struct {
	types.UnimplementedFeederStatusServer
	f fetcherInf
}

func NewStatusServer(f fetcherInf) *StatusServer {
	return &StatusServer{
		f: f,
	}
}

var _ types.FeederStatusServer = (*StatusServer)(nil)

// === gRPC Methods ===

func (s *StatusServer) HealthCheck(ctx context.Context, _ *types.Empty) (*types.HealthResponse, error) {
	return &types.HealthResponse{Alive: true}, nil
}

func (s *StatusServer) GetAllTokens(ctx context.Context, _ *types.Empty) (*types.GetAllTokensResponse, error) {
	status := s.f.GetTokensStatus()
	if len(status) == 0 {
		return nil, grpcstatus.Error(codes.NotFound, types.ErrStrNoTokensFound)
	}
	tokensRes := make([]*types.TokenInfo, 0, len(status))
	for sName, tokens := range status {
		for tName, token := range tokens {
			if strings.HasPrefix(tName, nstPrefix) {
				token.Price.Price = base64.StdEncoding.EncodeToString([]byte(token.Price.Price))
			}
			tokensRes = append(tokensRes, &types.TokenInfo{
				Source:    sName,
				Token:     tName,
				Price:     token.Price.Price,
				Decimals:  token.Price.Decimal,
				Timestamp: token.Price.Timestamp,
			})
		}
	}
	return &types.GetAllTokensResponse{
		Tokens: tokensRes,
	}, nil
}

func StartStatusServer(f fetcherInf, port int) chan error {
	errCh := make(chan error)
	srv := NewStatusServer(f)
	grpcSrv := grpc.NewServer()
	types.RegisterFeederStatusServer(grpcSrv, srv)
	if port <= 0 {
		port = types.DefaultPort
	}
	go func() {
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			errCh <- fmt.Errorf("failed to listen on port %d: %w", port, err)
			return
		}
		fmt.Printf("gRPC server listening on %s\n", lis.Addr().String())
		if err := grpcSrv.Serve(lis); err != nil {
			errCh <- fmt.Errorf("failed to serve gRPC server: %w", err)
		}
	}()
	return errCh
}
