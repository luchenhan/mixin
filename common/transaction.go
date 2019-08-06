package common

import (
	"crypto/rand"
	"fmt"

	"github.com/MixinNetwork/mixin/crypto"
)

const (
	TxVersion      = 0x01
	ExtraSizeLimit = 256

	OutputTypeScript           = 0x00
	OutputTypeWithdrawalSubmit = 0xa1
	OutputTypeWithdrawalFuel   = 0xa2
	OutputTypeNodePledge       = 0xa3
	OutputTypeNodeAccept       = 0xa4
	OutputTypeNodeDepart       = 0xa5
	OutputTypeNodeRemove       = 0xa6
	OutputTypeDomainAccept     = 0xa7
	OutputTypeDomainRemove     = 0xa8
	OutputTypeWithdrawalClaim  = 0xa9

	TransactionTypeScript           = 0x00
	TransactionTypeMint             = 0x01
	TransactionTypeDeposit          = 0x02
	TransactionTypeWithdrawalSubmit = 0x03
	TransactionTypeWithdrawalFuel   = 0x04
	TransactionTypeWithdrawalClaim  = 0x05
	TransactionTypeNodePledge       = 0x06
	TransactionTypeNodeAccept       = 0x07
	TransactionTypeNodeDepart       = 0x08
	TransactionTypeNodeRemove       = 0x09
	TransactionTypeDomainAccept     = 0x10
	TransactionTypeDomainRemove     = 0x11
	TransactionTypeUnknown          = 0xff
)

type Input struct {
	Hash    crypto.Hash  `json:"hash,omitempty"`
	Index   int          `json:"index,omitempty"`
	Genesis []byte       `json:"genesis,omitempty"`
	Deposit *DepositData `json:"deposit,omitempty"`
	Mint    *MintData    `json:"mint,omitempty"`
}

type Output struct {
	Type       uint8           `json:"type"`
	Amount     Integer         `json:"amount"`
	Keys       []crypto.Key    `json:"keys,omitempty"`
	Withdrawal *WithdrawalData `msgpack:",omitempty"json:"withdrawal,omitempty"`

	// OutputTypeScript fields
	Script Script     `json:"script,omitempty"`
	Mask   crypto.Key `json:"mask,omitempty"`
}

type Transaction struct {
	Version uint8       `json:"version"`
	Asset   crypto.Hash `json:"asset"`
	Inputs  []*Input    `json:"inputs"`
	Outputs []*Output   `json:"outputs"`
	Extra   []byte      `json:"extra,omitempty"`
}

type SignedTransaction struct {
	Transaction
	Signatures [][]crypto.Signature `json:"signatures,omitempty"`
}

func (tx *Transaction) ViewGhostKey(a *crypto.Key) []*Output {
	outputs := make([]*Output, 0)

	for i, o := range tx.Outputs {
		if o.Type != OutputTypeScript {
			continue
		}

		out := &Output{
			Type:   o.Type,
			Amount: o.Amount,
			Script: o.Script,
			Mask:   o.Mask,
		}
		for _, k := range o.Keys {
			key := crypto.ViewGhostOutputKey(&k, a, &o.Mask, uint64(i))
			out.Keys = append(out.Keys, *key)
		}
		outputs = append(outputs, out)
	}

	return outputs
}

func (tx *SignedTransaction) TransactionType() uint8 {
	for _, in := range tx.Inputs {
		if in.Mint != nil {
			return TransactionTypeMint
		}
		if in.Deposit != nil {
			return TransactionTypeDeposit
		}
		if in.Genesis != nil {
			return TransactionTypeUnknown
		}
	}

	isScript := true
	for _, out := range tx.Outputs {
		switch out.Type {
		case OutputTypeWithdrawalSubmit:
			return TransactionTypeWithdrawalSubmit
		case OutputTypeWithdrawalFuel:
			return TransactionTypeWithdrawalFuel
		case OutputTypeWithdrawalClaim:
			return TransactionTypeWithdrawalClaim
		case OutputTypeNodePledge:
			return TransactionTypeNodePledge
		case OutputTypeNodeAccept:
			return TransactionTypeNodeAccept
		case OutputTypeNodeDepart:
			return TransactionTypeNodeDepart
		case OutputTypeNodeRemove:
			return TransactionTypeNodeRemove
		case OutputTypeDomainAccept:
			return TransactionTypeDomainAccept
		case OutputTypeDomainRemove:
			return TransactionTypeDomainRemove
		}
		isScript = isScript && out.Type == OutputTypeScript
	}

	if isScript {
		return TransactionTypeScript
	}
	return TransactionTypeUnknown
}

func (signed *SignedTransaction) SignInput(reader UTXOReader, index int, accounts []Address) error {
	msg := MsgpackMarshalPanic(signed.Transaction)

	if len(accounts) == 0 {
		return nil
	}
	if index >= len(signed.Inputs) {
		return fmt.Errorf("invalid input index %d/%d", index, len(signed.Inputs))
	}
	in := signed.Inputs[index]
	if in.Deposit != nil || in.Mint != nil {
		return signed.SignRaw(accounts[0].PrivateSpendKey)
	}

	utxo, err := reader.ReadUTXO(in.Hash, in.Index)
	if err != nil {
		return err
	}
	if utxo == nil {
		return fmt.Errorf("input not found %s:%d", in.Hash.String(), in.Index)
	}

	keysFilter := make(map[string]bool)
	for _, k := range utxo.Keys {
		keysFilter[k.String()] = true
	}

	sigs := make([]crypto.Signature, 0)
	for _, acc := range accounts {
		priv := crypto.DeriveGhostPrivateKey(&utxo.Mask, &acc.PrivateViewKey, &acc.PrivateSpendKey, uint64(in.Index))
		if keysFilter[priv.Public().String()] {
			sigs = append(sigs, priv.Sign(msg))
		}
	}
	signed.Signatures = append(signed.Signatures, sigs)
	return nil
}

func (signed *SignedTransaction) SignRaw(key crypto.Key) error {
	msg := MsgpackMarshalPanic(signed.Transaction)

	if len(signed.Inputs) != 1 {
		return fmt.Errorf("invalid inputs count %d", len(signed.Inputs))
	}
	in := signed.Inputs[0]
	if in.Deposit == nil && in.Mint == nil {
		return fmt.Errorf("invalid input format")
	}
	if in.Deposit != nil {
		err := signed.verifyDepositFormat()
		if err != nil {
			return err
		}
	}
	signed.Signatures = append(signed.Signatures, []crypto.Signature{key.Sign(msg)})
	return nil
}

func NewTransaction(asset crypto.Hash) *Transaction {
	return &Transaction{
		Version: TxVersion,
		Asset:   asset,
	}
}

func (tx *Transaction) AddInput(hash crypto.Hash, index int) {
	in := &Input{
		Hash:  hash,
		Index: index,
	}
	tx.Inputs = append(tx.Inputs, in)
}

func (tx *Transaction) AddOutputWithType(ot uint8, accounts []Address, s Script, amount Integer, seed []byte) {
	out := &Output{
		Type:   ot,
		Amount: amount,
		Script: s,
		Keys:   make([]crypto.Key, 0),
	}

	if len(accounts) > 0 {
		r := crypto.NewKeyFromSeed(seed)
		out.Mask = r.Public()
		for _, a := range accounts {
			k := crypto.DeriveGhostPublicKey(&r, &a.PublicViewKey, &a.PublicSpendKey, uint64(len(tx.Outputs)))
			out.Keys = append(out.Keys, *k)
		}
	}

	tx.Outputs = append(tx.Outputs, out)
}

func (tx *Transaction) AddScriptOutput(accounts []Address, s Script, amount Integer, seed []byte) {
	tx.AddOutputWithType(OutputTypeScript, accounts, s, amount, seed)
}

func (tx *Transaction) AddRandomScriptOutput(accounts []Address, s Script, amount Integer) error {
	seed := make([]byte, 64)
	_, err := rand.Read(seed)
	if err != nil {
		return err
	}
	tx.AddScriptOutput(accounts, s, amount, seed)
	return nil
}
