package client

import (
	bctypes "github.com/QOSGroup/qbase/client/types"
	"github.com/spf13/cobra"
	"github.com/tendermint/go-amino"
)

func QueryCommands(cdc *amino.Codec) []*cobra.Command {
	return bctypes.GetCommands(
		queryInflationPhrasesCommand(cdc),
		queryTotalCommand(cdc),
		queryAppliedCommand(cdc),
	)
}
