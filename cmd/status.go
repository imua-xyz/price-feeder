package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	statustypes "github.com/imua-xyz/price-feeder/internal/status/types"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

var (
	defaultTimeout  = 3 * time.Second
	grpcAddr        string
	defaultGrpcAddr = fmt.Sprintf("localhost:%d", statustypes.DefaultPort)
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get the status of the price feeder",
	Long:  `Get the status of the price feeder`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		if grpcAddr == "" {
			grpcAddr = defaultGrpcAddr
		}
		conn, err := grpc.DialContext(
			ctx,
			grpcAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Println("Request timed out. Is the price feeder running?")
				return nil
			}
			return err
		}
		defer conn.Close()
		c := statustypes.NewFeederStatusClient(conn)
		res, err := c.GetAllTokens(context.Background(), &statustypes.Empty{})
		if err != nil {
			st, ok := status.FromError(err)
			if ok && st.Code() == codes.NotFound && st.Message() == statustypes.ErrStrNoTokensFound {
				fmt.Println("No valid token infos.")
				return nil
			}
			return err
		}
		printProto(res)
		return nil
	},
}
