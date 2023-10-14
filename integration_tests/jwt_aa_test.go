package integration_tests

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"os"
	"path"
	"testing"
	"time"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	xiontypes "github.com/burnt-labs/xion/x/xion/types"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/golang-jwt/jwt/v4"
	aatypes "github.com/larry0x/abstract-account/x/abstractaccount/types"
	"github.com/lestrrat-go/jwx/jwk"
	ibctest "github.com/strangelove-ventures/interchaintest/v7"
	"github.com/strangelove-ventures/interchaintest/v7/testutil"
	"github.com/stretchr/testify/require"
)

func TestXionDeployContract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	t.Parallel()

	td := BuildXionChain(t, "0.0uxion", ModifyInterChainGenesis(ModifyInterChainGenesisFn{ModifyGenesisShortProposals}, [][]string{{votingPeriod, maxDepositPeriod}}))
	xion, ctx := td.xionChain, td.ctx

	// Create and Fund User Wallets
	t.Log("creating and funding user accounts")
	fundAmount := int64(10_000_000)
	users := ibctest.GetAndFundTestUsers(t, ctx, "default", fundAmount, xion)
	xionUser := users[0]
	currentHeight, _ := xion.Height(ctx)
	testutil.WaitForBlocks(ctx, int(currentHeight)+8, xion)
	t.Logf("created xion user %s", xionUser.FormattedAddress())

	xionUserBalInitial, err := xion.GetBalance(ctx, xionUser.FormattedAddress(), xion.Config().Denom)
	require.NoError(t, err)
	require.Equal(t, fundAmount, xionUserBalInitial)

	// step 1: send a xion message with default (0%) platform fee
	recipientKeyName := "recipient-key"
	err = xion.CreateKey(ctx, recipientKeyName)
	require.NoError(t, err)
	receipientKeyAddressBytes, err := xion.GetAddress(ctx, recipientKeyName)
	require.NoError(t, err)
	recipientKeyAddress, err := types.Bech32ifyAddressBytes(xion.Config().Bech32Prefix, receipientKeyAddressBytes)
	require.NoError(t, err)

	xion.Config().EncodingConfig.InterfaceRegistry.RegisterImplementations(
		(*types.Msg)(nil),
		&xiontypes.MsgSetPlatformPercentage{},
		&xiontypes.MsgSend{},
		&wasmtypes.MsgInstantiateContract{},
		&wasmtypes.MsgStoreCode{},
		&aatypes.MsgUpdateParams{},
		&aatypes.MsgRegisterAccount{},
	)

	currentHeight, _ = xion.Height(ctx)
	_, err = ExecTx(t, ctx, xion.FullNodes[0],
		xionUser.KeyName(),
		"xion", "send", xionUser.KeyName(),
		"--chain-id", xion.Config().ChainID,
		recipientKeyAddress, fmt.Sprintf("%d%s", 100, xion.Config().Denom),
	)

	fp, err := os.Getwd()
	require.NoError(t, err)

	codeIDStr, err := xion.StoreContract(ctx, xionUser.FormattedAddress(),
		path.Join(fp, "integration_tests", "testdata", "contracts", "account.wasm"))
	require.NoError(t, err)

	codeResp, err := ExecQuery(t, ctx, xion.FullNodes[0],
		"wasm", "code-info", codeIDStr)
	require.NoError(t, err)
	t.Logf("code response: %s", codeResp)

	sub := "integration-test-user"
	aud := "integration-test-project"

	authenticatorDetails := map[string]string{}
	authenticatorDetails["sub"] = sub
	authenticatorDetails["aud"] = aud

	authenticator := map[string]interface{}{}
	authenticator["Jwt"] = authenticatorDetails

	instantiateMsg := map[string]interface{}{}
	instantiateMsg["id"] = 0
	instantiateMsg["authenticator"] = authenticator

	creatorAddr := types.AccAddress(xionUser.Address())
	codeHash, err := hex.DecodeString(codeResp["data_hash"].(string))
	require.NoError(t, err)
	t.Logf("predicting address")
	predictedAddr := wasmkeeper.BuildContractAddressPredictable(codeHash, creatorAddr, []byte("beef"), []byte{})

	privateKeyBz, err := os.ReadFile("./integration_tests/testdata/keys/jwtRS256.key")
	require.NoError(t, err)
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(privateKeyBz)
	require.NoError(t, err)
	t.Logf("private key: %v", privateKey)

	publicKey, err := jwk.New(privateKey)
	require.NoError(t, err)
	publicKeyJSON, err := json.Marshal(publicKey)
	require.NoError(t, err)

	t.Logf("public key: %s", publicKeyJSON)

	now := time.Now()
	fiveAgo := now.Add(-time.Minute * 5)
	inFive := now.Add(time.Minute * 5)

	auds := jwt.ClaimStrings{aud}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":              "stytch.com/project-test-185e9a9f-8bab-42f2-a924-953a59e8ff94",
		"sub":              sub,
		"aud":              auds,
		"exp":              inFive.Unix(),
		"nbf":              fiveAgo.Unix(),
		"iat":              fiveAgo.Unix(),
		"transaction_hash": predictedAddr.Bytes(),
	})

	output, err := token.SignedString(privateKey)
	require.NoError(t, err)

	instantiateMsg["signature"] = []byte(output)
	instantiateMsgStr, err := json.Marshal(instantiateMsg)
	require.NoError(t, err)
	t.Logf("inst msg: %s", string(instantiateMsgStr))

	t.Logf("registering account: %s", instantiateMsgStr)

	registeredTxHash, err := ExecTx(t, ctx, xion.FullNodes[0],
		xionUser.KeyName(),
		"abstract-account", "register",
		codeIDStr, string(instantiateMsgStr),
		"--salt", "beef",
		"--chain-id", xion.Config().ChainID,
	)

	require.NoError(t, err)
	t.Logf("hash: %s", registeredTxHash)
}
