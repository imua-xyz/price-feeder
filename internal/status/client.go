package status

import (
	"context"
	"errors"
	"time"

	statustypes "github.com/imua-xyz/price-feeder/internal/status/types"
	types "github.com/imua-xyz/price-feeder/internal/status/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

var defaultTimeout = 3 * time.Second

func GetAllTokens(addr string) (*types.GetAllTokensResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	c := statustypes.NewFeederStatusClient(conn)
	res, err := c.GetAllTokens(context.Background(), &statustypes.Empty{})
	if err != nil {
		return nil, err
	}
	return res, nil

}

func FilterErrors(err error) (string, error) {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "Request timed out. price-feeder is not running properly", nil
		}
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.NotFound && st.Message() == statustypes.ErrStrNoTokensFound {
			return "No valid token infos.", nil
		}
	}
	return "", err
}
