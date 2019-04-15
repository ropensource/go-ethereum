package core

import (
	"container/heap"
	"github.com/ethereum/go-ethereum/core/types"
)

type monitorHeap []*types.TransactionWrap

func (h monitorHeap) Len() int { return len(h) }

func (h monitorHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h monitorHeap) Less(i, j int) bool {
	return h[i].Bn < h[j].Bn
}

func (h *monitorHeap) Push(x interface{}) {
	*h = append(*h, x.(*types.TransactionWrap))
}

func (h *monitorHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type monitorList struct {
	all 	*monitorLookup
	items	*monitorHeap
	stales	int
}

func newMonitorList(all *monitorLookup) *monitorList {
	return &monitorList{
		all : all,
		items: new(monitorHeap),
	}
}

func (l *monitorList) Put(tx *types.TransactionWrap) {
	heap.Push(l.items, tx)
}

func (l *monitorList) Pop() *types.TransactionWrap {
	return heap.Pop(l.items).(*types.TransactionWrap)
}

















