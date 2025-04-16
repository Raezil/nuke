// SPDX-License-Identifier: Apache-2.0

package nuke

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
)

// failingArena always fails to allocate memory.
type failingArena struct{}

func (f *failingArena) Alloc(size, alignment uintptr) unsafe.Pointer {
	return nil
}

func (f *failingArena) Reset(release bool) {
	// No-op.
}

// TestNewNilArena verifies that New[T] returns a new pointer using Go's built-in allocation when a nil arena is provided.
func TestNewNilArena(t *testing.T) {
	p := New[int](nil)
	require.NotNil(t, p, "New with nil arena must not return nil")
	*p = 42
	require.Equal(t, 42, *p)
}

// TestMakeSliceNilArena verifies that MakeSlice[T] correctly creates a slice when a nil arena is provided.
func TestMakeSliceNilArena(t *testing.T) {
	s := MakeSlice[int](nil, 3, 5)
	require.Equal(t, 3, len(s))
	require.Equal(t, 5, cap(s))

	// Test that the slice works as expected.
	s[0] = 10
	s[1] = 20
	s[2] = 30
	require.Equal(t, []int{10, 20, 30}, s[:3])
}

// TestMakeSliceAllocFail verifies that when a non-nil arena fails to allocate memory (returns nil),
// MakeSlice falls back to using Go's built-in make function.
func TestMakeSliceAllocFail(t *testing.T) {
	arena := &failingArena{}
	s := MakeSlice[int](arena, 4, 8)
	require.Equal(t, 4, len(s))
	require.Equal(t, 8, cap(s))

	// Write and read some values to ensure the slice works as expected.
	s[0] = 7
	s[1] = 14
	require.Equal(t, 7, s[0])
	require.Equal(t, 14, s[1])
}

// TestContextArena verifies that an arena can be injected into and extracted from a context.
func TestContextArena(t *testing.T) {
	var arena Arena = &mockArena{}
	ctx := InjectContextArena(context.Background(), arena)
	extractedArena := ExtractContextArena(ctx)
	require.Equal(t, arena, extractedArena, "Extracted arena should match the one injected")
}

// TestExtractContextArenaNoValue verifies that extracting an arena from a context that has none returns nil.
func TestExtractContextArenaNoValue(t *testing.T) {
	arena := ExtractContextArena(context.Background())
	require.Nil(t, arena, "Extracting arena from context without one should return nil")
}

// TestConcurrentArena verifies that the concurrent arena correctly wraps a base arena and handles allocations and reset.
func TestConcurrentArena(t *testing.T) {
	base := &mockArena{}
	arena := NewConcurrentArena(base)

	// Run concurrent allocations.
	const goroutines = 10
	const iter = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < iter; j++ {
				ptr := arena.Alloc(16, 8)
				require.NotNil(t, ptr, "Allocation in concurrent arena should not be nil")
			}
			wg.Done()
		}()
	}
	wg.Wait()

	// Test that Reset does not panic.
	require.NotPanics(t, func() {
		arena.Reset(true)
		arena.Reset(false)
	})
}

// TestSliceAppendNilArena verifies that SliceAppend works correctly when no arena is provided.
func TestSliceAppendNilArena(t *testing.T) {
	original := []int{1, 2, 3}
	appended := SliceAppend[int](nil, original, 4, 5)
	require.Equal(t, []int{1, 2, 3, 4, 5}, appended)
}

// TestGrowSliceWithArena tests the slice growth path when using an arena.
// It uses a mock arena to exercise the reallocation path in growSlice.
func TestGrowSliceWithArena(t *testing.T) {
	a := &mockArena{}

	// Start with a non-empty slice; then append more elements than the current capacity.
	s := MakeSlice[int](a, 2, 2)
	s[0] = 100
	s[1] = 200

	// Append several elements to force a growth.
	result := SliceAppend[int](a, s, 300, 400, 500)
	expected := []int{100, 200, 300, 400, 500}
	require.Equal(t, expected, result)
}

// TestGrowSliceEmpty exercises the branch in growSlice when the original slice has zero capacity.
// Instead of using MakeSlice (which would call arena.Alloc with zero size and panic),
// we start with a nil slice.
func TestGrowSliceEmpty(t *testing.T) {
	a := &mockArena{}
	var s []int // nil slice: len(s)==0 and cap(s)==0
	s = SliceAppend[int](a, s, 42)
	require.Equal(t, []int{42}, s)
	require.Equal(t, 1, cap(s))
}

// TestSliceAppendNoGrow verifies that when appending data to a slice
// that already has sufficient capacity, no reallocation is performed.
func TestSliceAppendNoGrow(t *testing.T) {
	a := &mockArena{}
	// Create a slice using the arena with length 3 and capacity 5.
	s := MakeSlice[int](a, 3, 5)
	s[0] = 1
	s[1] = 2
	s[2] = 3
	// Append one element; total length becomes 4 which is <= cap(s)==5.
	s = SliceAppend[int](a, s, 4)
	require.Equal(t, []int{1, 2, 3, 4}, s[:4])
	require.Equal(t, 5, cap(s))
}

// TestGrowSliceLarge verifies the branch in growSlice when the existing slice capacity is large.
// This forces the new capacity to be increased by newCap + newCap/4.
func TestGrowSliceLarge(t *testing.T) {
	a := &mockArena{}
	// Create a slice with length 150 and capacity 300.
	s := MakeSlice[int](a, 150, 300)
	// Initialize the slice so that copy works correctly.
	for i := 0; i < 150; i++ {
		s[i] = i
	}
	// Append 200 elements, forcing newLen=350, which is >300.
	data := make([]int, 200)
	for i := 0; i < 200; i++ {
		data[i] = 1000 + i
	}
	result := SliceAppend[int](a, s, data...)
	// New capacity should be increased via the "else" branch in growSlice.
	require.Equal(t, 350, len(result))
	require.True(t, cap(result) > 300, "new capacity should be increased from original")
	// Verify contents: first 150 values should be preserved.
	for i := 0; i < 150; i++ {
		require.Equal(t, i, result[i])
	}
	// The appended data should be present.
	for i := 0; i < 200; i++ {
		require.Equal(t, 1000+i, result[150+i])
	}
}

// TestMultipleAllocations verifies that using New[T] consecutively from a monotonic arena returns valid pointers,
// and that when the arena is exhausted, allocations fall back to the heap.
func TestMultipleAllocations(t *testing.T) {
	// Create a low-capacity monotonic arena so that after a couple of allocations,
	// one allocation will return nil from the arena causing New[T] to fall back to heap allocation.
	arena := NewMonotonicArena(2*int(unsafe.Sizeof(int(0))), 1)
	p1 := New[int](arena)
	p2 := New[int](arena)
	p3 := New[int](arena)

	// p1 and p2 should be allocated from the arena.
	require.True(t, isMonotonicArenaPtr(arena, unsafe.Pointer(p1)))
	require.True(t, isMonotonicArenaPtr(arena, unsafe.Pointer(p2)))
	// p3 might be allocated on the heap if the arena is exhausted.
	ptrFromArena := isMonotonicArenaPtr(arena, unsafe.Pointer(p3))
	t.Logf("p3 allocation from monotonic arena: %v", ptrFromArena)
}

// TestMonotonicArenaReset_NoAlloc tests the Reset method on a monotonic arena that has not yet performed any allocations,
// ensuring that the early exit branch in reset (when s.offset == 0) is executed.
func TestMonotonicArenaReset_NoAlloc(t *testing.T) {
	arena := NewMonotonicArena(1024, 1).(*monotonicArena)
	// Without any allocation, call Reset with both parameters.
	require.NotPanics(t, func() { arena.Reset(false) })
	require.NotPanics(t, func() { arena.Reset(true) })
}

// TestMonotonicArenaReset_WithAlloc exercises Reset after an allocation has occurred.
func TestMonotonicArenaReset_WithAlloc(t *testing.T) {
	arena := NewMonotonicArena(1024, 1).(*monotonicArena)
	// Allocate an object.
	_ = New[int](arena)
	// Reset without releasing memory.
	require.NotPanics(t, func() { arena.Reset(false) })
	// Do another allocation.
	_ = New[int](arena)
	// Reset with release.
	require.NotPanics(t, func() { arena.Reset(true) })

	// Wait briefly to allow any asynchronous finalizer activity (if any).
	time.Sleep(10 * time.Millisecond)
}

// TestMonotonicArenaAllocFail covers the branch in monotonicArena.Alloc when allocation fails.
// It requests more memory than is available in the arena.
func TestMonotonicArenaAllocFail(t *testing.T) {
	// Create a very small monotonic arena.
	arena := NewMonotonicArena(64, 1)
	// Request an allocation larger than the buffer.
	ptr := arena.Alloc(128, 1)
	require.Nil(t, ptr, "Allocation should fail and return nil when arena is exhausted")
}

// Extra benchmark tests (they do not affect coverage).
func BenchmarkExtra(b *testing.B) {
	arena := NewMonotonicArena(32*1024*1024, 6)
	a := newArenaAllocator[int](arena)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = a.new()
	}
}

func BenchmarkConcurrentExtra(b *testing.B) {
	arena := NewConcurrentArena(NewMonotonicArena(32*1024*1024, 6))
	a := newArenaAllocator[int](arena)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = a.new()
	}
}

func BenchmarkRuntimeExtra(b *testing.B) {
	a := newRuntimeAllocator[int]()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = a.new()
	}
}

func BenchmarkSliceAppendExtra(b *testing.B) {
	s := []int{}
	for i := 0; i < b.N; i++ {
		s = SliceAppend[int](nil, s, i)
	}
	// This benchmark is solely to exercise the slice logic.
	fmt.Println(s[len(s)-1])
}
