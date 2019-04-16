package core

import (
	"bytes"
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

var MONITOR_POOL *MonitorPool

const (
	MinBlockConfirms        int64 = 20
	DEFAULT_ACTOR_FILE_NAME       = ".actor"
)

type monitorBlockChain interface {
	CurrentBlock() *types.Block
	GetBlock(hash common.Hash, number uint64) *types.Block

	SubscribeChainHeadEvent(ch chan<- ChainHeadEvent) event.Subscription
}

type MonitorPoolConfig struct {
	MonitorEnable bool // the switch of monitor compute

	NoLocals    bool          // Whether local transaction handling should be disabled
	Journal     string        // Journal of local transactions to survive node restarts
	Rejournal   time.Duration // Time interval to regenerate the local transaction journal
	GlobalQueue uint64        // Maximum number of non-executable transaction slots for all accounts
	Lifetime    time.Duration // Maximum amount of time non-executable transaction are queued

	LocalRpcPort int            // LocalRpcPort of local rpc port
	IceConf      string         // ice conf to init vm
	MonitorActor common.Address // the actor of monitor compute

	MonitorKeys []*keystore.Key
	ProxyAddr	common.Address
}

var DefaultMonitorHexKey = []string{
	"0x668a369e87c01da5bfca9851e6ee86d760e17ee7912d77b7dffe8e0cdf63bcb5",
}

var DefaultMonitorPoolConfig = MonitorPoolConfig{
	Journal:     "monitor_transactions.rlp",
	Rejournal:   time.Second * 4,
	GlobalQueue: 1024,
	Lifetime:    3 * time.Hour,
	ProxyAddr:   common.HexToAddress("0x8548eCCf35c9f41B9a0aA30483FA5B3d75D2CC11"),
	MonitorKeys: make([]*keystore.Key, 0),
}

type MonitorPool struct {
	config      MonitorPoolConfig
	chainconfig *params.ChainConfig
	chain       monitorBlockChain

	mu      sync.RWMutex
	journal *monitorJournal // Journal of local monitor transaction to back up to disk
	all     *monitorLookup  // All transactions to allow lookups
	queue   *monitorList    // All transactions sorted by price

	quiteSign chan interface{}

	// ethclient
	client 	*ethclient.Client

	wg sync.WaitGroup // for shutdown sync
}

func NewMonitorPool(config MonitorPoolConfig, chainconfig *params.ChainConfig, chain monitorBlockChain) *MonitorPool {
	pool := &MonitorPool{
		config:      config,
		chainconfig: chainconfig,
		chain:       chain,
		all:         newMonitorLookup(),
	}

	pool.queue = newMonitorList(pool.all)
	if config.Journal != "" {
		pool.journal = newMonitorJournal(config.Journal)
		if err := pool.journal.load(pool.AddLocals); err != nil {
			log.Warn("Failed to load monitor transaction journal", "err", err)
		}
		if err := pool.journal.rotate(pool.local()); err != nil {
			log.Warn("Failed to rotate monitor transaction journal", "err", err)
		}
	}

	pool.wg.Add(1)
	go pool.loop()

	// save to global attr
	MONITOR_POOL = pool

	// init mvm
	url := "http://127.0.0.1:" + strconv.FormatUint(uint64(pool.config.LocalRpcPort), 10)

	// 拨号动作是否需要延后
	cli, err := ethclient.Dial(url)
	if err != nil {
		log.Error("Init ethclient fail.", err)
		panic("Can't init ethclient.")
	}
	pool.client = cli

	return pool
}

func (pool *MonitorPool) loop() {

	defer pool.wg.Done()

	var prevQueued int

	report := time.NewTicker(statsReportInterval)
	defer report.Stop()

	journal := time.NewTicker(pool.config.Rejournal)
	defer journal.Stop()

	pop := time.NewTicker(time.Second * 1)

CONTINUE:
	// Keep waiting for and reacting to the various events
	for {
		select {
		case <-report.C:
			pool.mu.RLock()
			_, queued := pool.stats()
			pool.mu.RUnlock()
			if queued != prevQueued {
				log.Debug("Monitor transaction pool status report", "queued", queued)
				prevQueued = queued
			}

		// Handle local monitor transaction journal rotation
		case <-journal.C:
			if pool.journal != nil {
				pool.mu.Lock()
				if err := pool.journal.rotate(pool.local()); err != nil {
					log.Warn("Failed to rotate local tx journal", "err", err)
				}
				pool.mu.Unlock()
			}

		case <-pool.quiteSign:
			return

		case <-pop.C:
			if pool.queue.items.Len() != 0 {
				pool.mu.Lock()
				tx := pool.queue.Pop()
				bn := pool.chain.CurrentBlock().Number()
				pool.all.Remove(tx.Hash())
				pool.mu.Unlock()

				// 构造交易，签名，发送
				var(
					value = new(big.Int)
					fee = new(big.Int)
					gasPrice = big.NewInt(5420000000)
					addGasPrice = big.NewInt(5430000000)
					context = context.Background()
				)
				fee.Sub(big.NewInt(int64(params.TxGas)), addGasPrice)
				value.Sub(tx.Value(), fee)
				nonce, err := pool.client.NonceAt(context, pool.config.ProxyAddr, bn)
				if err == nil {
					log.Error("get nonce fail.", "address", pool.config.ProxyAddr.Hex())
					continue CONTINUE
				}
				signer := types.MakeSigner(pool.chainconfig, bn)
				unSignTx := types.NewTransaction(nonce, pool.config.ProxyAddr, value, params.TxGas, gasPrice, nil)
				signTx, err := types.SignTx(unSignTx, signer, pool.config.MonitorKeys[tx.KeyIndex].PrivateKey)
				err = pool.client.SendTransaction(context, signTx)
				if err != nil {
					log.Error("Send proxy txfail.", "txHash", signTx.Hash().Hex())
					continue CONTINUE
				}
				log.Info("ProxyForward tx.", "Hash", signTx.Hash().Hex())
			}
		}
	}
}

func (pool *MonitorPool) LoadActor() error {
	absPath, err := filepath.Abs(DEFAULT_ACTOR_FILE_NAME)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return nil
	}
	res, _ := ioutil.ReadFile(absPath)
	pool.config.MonitorActor = common.BytesToAddress(res)
	fmt.Println(pool.config.MonitorActor.Hex())
	return nil
}

// Stop terminates the monitor transaction pool.
func (pool *MonitorPool) Stop() {
	pool.quiteSign <- true
	pool.wg.Wait()

	if pool.journal != nil {
		pool.journal.close()
	}
	log.Info("Transaction pool stopped")
}

// SubscribeNewTxsEvent registers a subscription of NewTxsEvent and
// starts sending event to the given channel.
func (pool *MonitorPool) SubscribeNewTxsEvent(ch chan<- NewTxsEvent) event.Subscription {
	//return pool.scope.Track(pool.txFeed.Subscribe(ch))
	return nil
}

func (pool *MonitorPool) AddLocals(txs []*types.TransactionWrap) []error {
	return pool.addTxs(txs)
}

func (pool *MonitorPool) addTxs(txs []*types.TransactionWrap) []error {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	return pool.addTxsLocked(txs)
}

func (pool *MonitorPool) addTxsLocked(txs []*types.TransactionWrap) []error {
	errs := make([]error, len(txs))
	for i, tx := range txs {
		var replace bool
		if replace, errs[i] = pool.add(tx); errs[i] == nil && !replace {
			log.Warn("load txs from rlp fail", "hash", tx.Hash())
		}
	}
	return errs
}

func (pool *MonitorPool) Stats() (int, int) {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.stats()
}

func (pool *MonitorPool) stats() (int, int) {
	return 0, len(pool.queue.all.txs)
}

func (pool *MonitorPool) local() types.TransactionWraps {
	monitorTxs := make([]*types.TransactionWrap, 0)
	for _, v := range pool.queue.all.txs {
		monitorTxs = append(monitorTxs, v)
	}
	return monitorTxs
}

func (pool *MonitorPool) InjectTxs(block *types.Block, receipts types.Receipts, bc *BlockChain, state *state.StateDB) {
	if !pool.config.MonitorEnable {
		log.Debug("Wow ~ Monitor Disable...")
		return
	}
	for _, tx := range block.Transactions() {
		isSave := false
		var (
			taskId string
			keyIndex uint64
			)
		for _, receipt := range receipts {
			if bytes.Equal(receipt.TxHash.Bytes(), tx.Hash().Bytes()) {
				switch {
				case tx.Value().Int64() == 0, receipt.Status != 0:
					break
				}
				if idx, res := pool.validTx(tx, receipt); res {
					keyIndex = uint64(idx)
				}
			}
		}
		if isSave {
			wrap := &types.TransactionWrap{
				Transaction: tx,
				Bn:          block.NumberU64(),
				TaskId:      taskId,
				KeyIndex: 	 keyIndex,
			}
			pool.add(wrap)
		}
	}
}

// 校验有效交易
func(pool *MonitorPool) validTx(tx *types.Transaction, receipt *types.Receipt) (idx int, res bool) {
	for index, key := range pool.config.MonitorKeys {
		if key.Address == *tx.To() {
			return index, true
		}
	}
	return -1, false
}

func (pool *MonitorPool) add(tx *types.TransactionWrap) (bool, error) {

	hash := tx.Hash()
	if pool.all.Get(hash) != nil {
		log.Trace("God ~ Discarding already known transaction", "hash", hash)
		return false, fmt.Errorf("known monitor transaction: %x", hash)
	}

	replace, err := pool.enqueueTx(hash, tx)
	if err != nil {
		return false, err
	}

	pool.journalTx(tx)

	log.Trace("Pooled new future monitor transaction", "hash", hash, "to", tx.To())
	return replace, nil
}

func (pool *MonitorPool) enqueueTx(hash common.Hash, tx *types.TransactionWrap) (bool, error) {
	if pool.all.Get(hash) == nil {
		pool.all.Add(tx)
		pool.queue.items.Push(tx)
	}
	return true, nil
}

// Write local file
func (pool *MonitorPool) journalTx(tx *types.TransactionWrap) {
	if pool.journal == nil {
		return
	}
	if err := pool.journal.insert(tx); err != nil {
		log.Warn("Failed to journal local transaction", "err", err)
	}
}

// addTx enqueues a single transaction into the pool if it is valid.
func (pool *MonitorPool) addTx(tx *types.TransactionWrap) error {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	// Try to inject the transaction and update any state
	_, err := pool.add(tx)
	if err != nil {
		return err
	}
	return nil
}

type monitorLookup struct {
	txs  map[common.Hash]*types.TransactionWrap
	lock sync.RWMutex
}

// newTxLookup returns a new monitorLookup structure.
func newMonitorLookup() *monitorLookup {
	return &monitorLookup{
		txs: make(map[common.Hash]*types.TransactionWrap),
	}
}

// Range calls f on each key and value present in the map.
func (t *monitorLookup) Range(f func(hash common.Hash, tx *types.TransactionWrap) bool) {
	t.lock.RLock()
	defer t.lock.RUnlock()

	for key, value := range t.txs {
		if !f(key, value) {
			break
		}
	}
}

// Get returns a transaction if it exists in the lookup, or nil if not found.
func (t *monitorLookup) Get(hash common.Hash) *types.TransactionWrap {
	t.lock.RLock()
	defer t.lock.RUnlock()

	return t.txs[hash]
}

// Count returns the current number of items in the lookup.
func (t *monitorLookup) Count() int {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return len(t.txs)
}

// Add adds a transaction to the lookup.
func (t *monitorLookup) Add(tx *types.TransactionWrap) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.txs[tx.Hash()] = tx
}

// Remove removes a transaction from the lookup.
func (t *monitorLookup) Remove(hash common.Hash) {
	t.lock.Lock()
	defer t.lock.Unlock()
	delete(t.txs, hash)
}

