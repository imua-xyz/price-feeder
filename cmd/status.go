package cmd

import (
	"fmt"
	"time"

	istatus "github.com/imua-xyz/price-feeder/internal/status"
	statustypes "github.com/imua-xyz/price-feeder/internal/status/types"
	"github.com/spf13/cobra"
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
		if grpcAddr == "" {
			grpcAddr = defaultGrpcAddr
		}
		res, err := istatus.GetAllTokens(grpcAddr)
		if err != nil {
			message, err := istatus.FilterErrors(err)
			if err != nil {
				return fmt.Errorf("error getting status: %w", err)
			}
			fmt.Printf("Status:%s\n", message)
			return nil
		}
		printProto(res)
		return nil
	},
}
