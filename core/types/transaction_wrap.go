package types

import (
	"github.com/ethereum/go-ethereum/rlp"
	"io"
)

type TransactionWrap struct {
	*Transaction
	Bn 			uint64
	FuncName 	string
	TaskId		string
	KeyIndex	uint64
}

type extwrapper struct {
	Transaction *Transaction
	Bn 			uint64
	FuncName 	string
	TaskId		string
	KeyIndex	uint64
}

func (t *TransactionWrap) GetBlockNumber() uint64 {
	return t.Bn
}

func (t *TransactionWrap) GetKeyIndex() uint64 {
	return t.KeyIndex
}

func (t *TransactionWrap) DecodeRLP(s *rlp.Stream) error {
	var ew extwrapper
	if err := s.Decode(&ew); err != nil {
		return err
	}
	t.Transaction, t.Bn, t.FuncName, t.TaskId, t.KeyIndex = ew.Transaction, ew.Bn, ew.FuncName, ew.TaskId, ew.KeyIndex
	//fmt.Println("Decode RLP, bn:", t.Bn)
	return nil
}

func (b *TransactionWrap) EncodeRLP(w io.Writer) error {
	//fmt.Println("EncodeRlp, bn:", b.Bn)
	return rlp.Encode(w, extwrapper{
		Transaction: b.Transaction,
		Bn: b.Bn,
		FuncName: b.FuncName,
		TaskId: b.TaskId,
		KeyIndex: b.KeyIndex,
	})
}

type TransactionWraps []*TransactionWrap

func (s TransactionWraps) Len() int { return len(s) }

func (s TransactionWraps) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

func (s TransactionWraps) GetRlp(i int) []byte {
	enc, _ := rlp.EncodeToBytes(s[i])
	return enc
}