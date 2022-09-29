// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

type targetBucket struct {
	targets     []target
	totalWeight float64
}

// targetBucketHeap implements heap.Interface for lowest total bucket weight. Implementation follows the
// example at https://pkg.go.dev/container/heap.
type targetBucketHeap []targetBucket

func (b targetBucketHeap) Len() int           { return len(b) }
func (b targetBucketHeap) Less(i, j int) bool { return b[i].totalWeight < b[j].totalWeight }
func (b targetBucketHeap) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func (b *targetBucketHeap) Push(x any) {
	*b = append(*b, x.(targetBucket))
}

func (b *targetBucketHeap) Pop() any {
	old := *b
	n := len(old)
	x := old[n-1]
	*b = old[0 : n-1]
	return x
}
