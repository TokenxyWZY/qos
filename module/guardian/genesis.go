package guardian

import (
	"github.com/QOSGroup/qbase/context"
	"github.com/QOSGroup/qos/module/guardian/types"
)

// 初始化创世数据
func InitGenesis(ctx context.Context, data types.GenesisState) {
	err := types.ValidateGenesis(data)
	if err != nil {
		panic(err)
	}

	mapper := GetMapper(ctx)
	for _, guardian := range data.Guardians {
		mapper.AddGuardian(guardian)
	}
}

// 导出状态数据
func ExportGenesis(ctx context.Context) types.GenesisState {
	mapper := GetMapper(ctx)
	iterator := mapper.GuardiansIterator()
	defer iterator.Close()
	var guardians []types.Guardian
	for ; iterator.Valid(); iterator.Next() {
		var guardian types.Guardian
		mapper.GetCodec().MustUnmarshalBinaryBare(iterator.Value(), &guardian)
		guardians = append(guardians, guardian)
	}

	return types.NewGenesisState(guardians)
}
