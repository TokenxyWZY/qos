package gov

import (
	"fmt"
	"github.com/QOSGroup/qos/module/distribution"
	"github.com/QOSGroup/qos/module/gov/mapper"
	"github.com/QOSGroup/qos/module/gov/types"
	"github.com/QOSGroup/qos/module/mint"
	"github.com/QOSGroup/qos/module/params"
	"github.com/QOSGroup/qos/module/stake"
	"github.com/QOSGroup/qos/version"

	"github.com/QOSGroup/qbase/account"
	"github.com/QOSGroup/qbase/context"
	btypes "github.com/QOSGroup/qbase/types"
	qtypes "github.com/QOSGroup/qos/types"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/log"
)

func BeginBlocker(ctx context.Context, req abci.RequestBeginBlock) {
	logger := ctx.Logger().With("module", "module/gov")
	if ctx.BlockHeight() > 1 {
		// software upgrade
		proposal := types.Proposal{}
		exists := GetMapper(ctx).Get(types.KeySoftUpgradeProposal, &proposal)
		if exists {
			proposalContent := proposal.ProposalContent.(*types.SoftwareUpgradeProposal)

			// start network from zero height
			if proposalContent.ForZeroHeight {
				panic(fmt.Sprintf("PLEASE INSTALL VERSION: %s, THEN SET THE CORRECT `genesis.json`(DataHeight:%d, MD5:%s) FOR UPGRADING TO %s, YOU CAN DOWNLOAD THE `genesis.json` FROM %s",
					proposalContent.Version, proposalContent.DataHeight, proposalContent.GenesisMD5, proposalContent.Version, proposalContent.GenesisFile))
			}

			// need the new software version
			if version.Version != proposalContent.Version {
				panic(fmt.Sprintf("PLEASE INSTALL VERSION: %s , THEN RESTART YOUR NODE FOR THE SOFTWARE UPGRADING", proposalContent.Version))
			}

			GetMapper(ctx).Del(types.KeySoftUpgradeProposal)
			logger.Info("software upgrade completed", "proposal", proposal.ProposalID)
		}
	}
}

// Process proposals
func EndBlocker(ctx context.Context) {
	logger := ctx.Logger().With("module", "module/gov")

	gm := mapper.GetMapper(ctx)
	inactiveIterator := gm.InactiveProposalQueueIterator(ctx.BlockHeader().Time)
	defer inactiveIterator.Close()
	for ; inactiveIterator.Valid(); inactiveIterator.Next() {
		var proposalID int64

		gm.GetCodec().UnmarshalBinaryBare(inactiveIterator.Value(), &proposalID)
		inactiveProposal, ok := gm.GetProposal(proposalID)
		if !ok {
			panic(fmt.Sprintf("proposal %d does not exist", proposalID))
		}

		gm.DeleteProposal(proposalID)
		gm.DeleteDeposits(ctx, proposalID) // delete any associated deposits (burned)
		gm.Metrics.ProposalStatus.With(mapper.ProposalIdLabel, fmt.Sprintf("%d", proposalID)).Set(float64(types.StatusNil))

		ctx.EventManager().EmitEvent(
			btypes.NewEvent(
				types.EventTypeInactiveProposal,
				btypes.NewAttribute(types.AttributeKeyProposalID, fmt.Sprintf("%d", proposalID)),
				btypes.NewAttribute(types.AttributeKeyProposalResult, types.AttributeKeyDropped),
			),
		)

		logger.Info(
			fmt.Sprintf("proposal %d (%s) didn't meet minimum deposit of %d (had only %d); deleted",
				inactiveProposal.ProposalID,
				inactiveProposal.GetTitle(),
				gm.GetLevelParams(ctx, inactiveProposal.GetProposalLevel()).MinDeposit,
				inactiveProposal.TotalDeposit,
			),
		)
	}

	// fetch active proposals whose voting periods have ended (are passed the block time)
	activeIterator := gm.ActiveProposalQueueIterator(ctx.BlockHeader().Time)
	defer activeIterator.Close()
	for ; activeIterator.Valid(); activeIterator.Next() {
		var proposalID int64

		gm.GetCodec().UnmarshalBinaryBare(activeIterator.Value(), &proposalID)
		activeProposal, ok := gm.GetProposal(proposalID)
		if !ok {
			panic(fmt.Sprintf("proposal %d does not exist", proposalID))
		}

		proposalResult, tallyResults, votingValidators, deductOption := mapper.Tally(ctx, gm, activeProposal)

		switch deductOption {
		case types.DepositDeductNone:
			gm.RefundDeposits(ctx, activeProposal.ProposalID, activeProposal.GetProposalLevel(), false)
			break
		case types.DepositDeductPart:
			gm.RefundDeposits(ctx, activeProposal.ProposalID, activeProposal.GetProposalLevel(), true)
			break
		case types.DepositDeductAll:
			gm.DeleteDeposits(ctx, activeProposal.ProposalID)
			break
		}

		var tagValue string
		switch proposalResult {
		case types.PASS:
			activeProposal.Status = types.StatusPassed
			tagValue = types.AttributeKeyResultPassed
			Execute(ctx, activeProposal, logger)
			break
		case types.REJECT:
			activeProposal.Status = types.StatusRejected
			tagValue = types.AttributeKeyResultRejected
			break
		case types.REJECTVETO:
			activeProposal.Status = types.StatusRejected
			tagValue = types.AttributeKeyResultVetoRejected
			break
		}

		activeProposal.FinalTallyResult = tallyResults
		gm.SetProposal(activeProposal)
		gm.RemoveFromActiveProposalQueue(activeProposal.VotingEndTime, activeProposal.ProposalID)
		gm.Metrics.ProposalStatus.With(mapper.ProposalIdLabel, fmt.Sprintf("%d", proposalID)).Set(float64(activeProposal.Status))

		logger.Info(
			fmt.Sprintf(
				"proposal %d (%s) tallied; result: %v",
				activeProposal.ProposalID, activeProposal.GetTitle(), proposalResult,
			),
		)

		ctx.EventManager().EmitEvent(
			btypes.NewEvent(
				types.EventTypeActiveProposal,
				btypes.NewAttribute(types.AttributeKeyProposalID, fmt.Sprintf("%d", proposalID)),
				btypes.NewAttribute(types.AttributeKeyProposalResult, tagValue),
			),
		)

		// slash not voted validators
		penalty := gm.GetLevelParams(ctx, activeProposal.GetProposalLevel()).Penalty
		if penalty.GT(qtypes.ZeroDec()) {
			sm := stake.GetMapper(ctx)
			var validators []stake.Validator
			sm.Get(stake.BuildCurrentValidatorsAddressKey(), &validators)
			for _, val := range validators {
				if _, ok := votingValidators[val.GetValidatorAddress().String()]; !ok {
					if validator, exists := sm.GetValidator(val.GetValidatorAddress()); exists && validator.BondHeight < activeProposal.VotingStartHeight {
						e := slash(ctx, validator, penalty, activeProposal.ProposalID)
						if e != nil {
							logger.Error("slash validator error", "e", e, "validator", validator.GetValidatorAddress().String())
						}
					}
				}
			}
		}

		gm.DeleteValidatorSet(proposalID)
	}

}

func slash(ctx context.Context, validator stake.Validator, penalty qtypes.Dec, proposalID int64) error {

	log := ctx.Logger().With("module", "module/gov")

	log.Debug("slash validator", "proposalId", proposalID, "validator", validator.GetValidatorAddress().String())

	sm := stake.GetMapper(ctx)
	var delegations []stake.Delegation
	sm.IterateDelegationsValDeleAddr(validator.GetValidatorAddress(), func(valAddr btypes.ValAddress, delAddr btypes.AccAddress) {
		if delegation, exists := sm.GetDelegationInfo(delAddr, valAddr); exists {
			delegations = append(delegations, delegation)
		}
	})

	totalSlashTokens := btypes.ZeroInt()
	log.Debug("slash delegations", "delegations", delegations)

	for _, delegation := range delegations {
		bondTokens := delegation.Amount

		// 计算惩罚
		tokenSlashed := qtypes.NewDecFromInt(bondTokens).Mul(penalty).TruncateInt()
		if !tokenSlashed.LT(bondTokens) {
			tokenSlashed = bondTokens
		}
		totalSlashTokens = totalSlashTokens.Add(tokenSlashed)

		// 修改delegator绑定收益
		remainTokens := bondTokens.Sub(tokenSlashed)
		delegation.Amount = remainTokens
		sm.BeforeDelegationModified(ctx, validator.GetValidatorAddress(), delegation.DelegatorAddr, delegation.Amount)

		// 更新delegation
		sm.SetDelegationInfo(delegation)

		log.Debug("slash validator's delegators", "delegator", delegation.DelegatorAddr.String(), "preToken", bondTokens, "slashToken", tokenSlashed, "remainTokens", remainTokens)
	}

	if totalSlashTokens.GT(validator.BondTokens) {
		panic("slash token is greater then validator bondTokens")
	}

	// 更新validator
	sm.ChangeValidatorBondTokens(validator, validator.BondTokens.Sub(totalSlashTokens))
	log.Debug("slash validator bond tokens", "validator", validator.GetValidatorAddress().String(), "preTokens", validator.BondTokens, "slashTokens", totalSlashTokens, "afterTokens", validator.BondTokens.Sub(totalSlashTokens))

	// slash放入社区费池
	sm.AfterValidatorSlashed(ctx, totalSlashTokens)

	return nil
}

func Execute(ctx context.Context, proposal types.Proposal, logger log.Logger) error {
	switch proposal.GetProposalType() {
	case types.ProposalTypeParameterChange:
		return executeParameterChange(ctx, proposal, logger)
	case types.ProposalTypeTaxUsage:
		return executeTaxUsage(ctx, proposal, logger)
	case types.ProposalTypeModifyInflation:
		return executeModifyInflation(ctx, proposal, logger)
	case types.ProposalTypeSoftwareUpgrade:
		GetMapper(ctx).Set(types.KeySoftUpgradeProposal, proposal)
		return nil
	}

	return nil
}

func executeParameterChange(ctx context.Context, proposal types.Proposal, logger log.Logger) error {
	proposalContent := proposal.ProposalContent.(*types.ParameterProposal)
	paramMapper := params.GetMapper(ctx)
	for _, param := range proposalContent.Params {
		paramSet, exists := paramMapper.GetModuleParamSet(param.Module)
		if !exists {
			panic(fmt.Sprintf("%s should exists", param.Module))
		}
		v, _ := paramSet.ValidateKeyValue(param.Key, param.Value)
		paramMapper.SetParam(param.Module, param.Key, v)
	}

	logger.Info("execute parameterChange", "proposal", proposal.ProposalID)

	return nil
}

func executeTaxUsage(ctx context.Context, proposal types.Proposal, logger log.Logger) error {
	proposalContent := proposal.ProposalContent.(*types.TaxUsageProposal)
	dm := distribution.GetMapper(ctx)
	accountMapper := ctx.Mapper(account.AccountMapperName).(*account.AccountMapper)
	acc := accountMapper.GetAccount(proposalContent.DestAddress)
	var account *qtypes.QOSAccount
	if acc == nil {
		account = qtypes.NewQOSAccountWithAddress(proposalContent.DestAddress)
	} else {
		account = acc.(*qtypes.QOSAccount)
	}
	feePool := dm.GetCommunityFeePool()
	qos := proposalContent.Percent.MulInt(feePool).TruncateInt()

	if feePool.LT(qos) {
		return fmt.Errorf("Percent %s may be too bigger. feePoo %s, usage %s", proposalContent.Percent, feePool, qos)
	}

	account.MustPlusQOS(qos)
	accountMapper.SetAccount(account)

	dm.SetCommunityFeePool(feePool.Sub(qos))

	logger.Info("execute taxUsage", "proposal", proposal.ProposalID)

	return nil
}

func executeModifyInflation(ctx context.Context, proposal types.Proposal, logger log.Logger) error {
	proposalContent := proposal.ProposalContent.(*types.ModifyInflationProposal)
	mintMapper := mint.GetMapper(ctx)
	oldPhrases := mintMapper.MustGetInflationPhrases()
	phrases := proposalContent.InflationPhrases.Adapt(oldPhrases)
	mintMapper.SetInflationPhrases(phrases)
	mintMapper.SetTotalQOSAmount(proposalContent.TotalAmount)
	logger.Info("execute modifyInflation, proposal: %d", proposal.ProposalID)

	return nil
}
