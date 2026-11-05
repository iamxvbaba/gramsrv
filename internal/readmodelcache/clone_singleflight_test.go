package readmodelcache

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func TestSingleflightClonesEachCallerResult(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		name := "unversioned"
		if versioned {
			name = "versioned"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := New[int, []int](Config[int, []int]{
					MaxEntries: 4,
					Clone:      slices.Clone[[]int],
				})
				source := []int{1, 2, 3}
				release := make(chan struct{})
				var calls atomic.Int32
				load := func() ([]int, error) {
					calls.Add(1)
					<-release
					return source, nil
				}
				type result struct {
					value []int
					err   error
				}
				results := make(chan result, 2)
				for range 2 {
					go func() {
						var v []int
						var err error
						if versioned {
							v, err = c.GetOrLoadVersioned(context.Background(), 1, 42, load)
						} else {
							v, err = c.GetOrLoad(context.Background(), 1, load)
						}
						results <- result{value: v, err: err}
					}()
				}
				// The loader is blocked on release and the other caller has joined
				// its singleflight before either can receive a result.
				synctest.Wait()
				close(release)
				first, second := <-results, <-results
				if first.err != nil || second.err != nil {
					t.Fatalf("GetOrLoad errors: %v, %v", first.err, second.err)
				}
				if calls.Load() != 1 {
					t.Fatalf("load calls = %d, want 1", calls.Load())
				}
				want := []int{1, 2, 3}
				if !slices.Equal(first.value, want) || !slices.Equal(second.value, want) {
					t.Fatalf("initial results = %v, %v, want %v", first.value, second.value, want)
				}
				first.value[0] = 99
				if !slices.Equal(second.value, want) {
					t.Fatalf("mutating one caller's result changed another caller's result: %v", second.value)
				}
				second.value[1] = 88
				source[2] = 77
				if !slices.Equal(first.value, []int{99, 2, 3}) {
					t.Fatalf("first result changed through another alias: %v", first.value)
				}
				if got, ok := c.Peek(1); !ok || !slices.Equal(got, want) {
					t.Fatalf("cached value = %v, %v, want %v, true", got, ok, want)
				}
			})
		})
	}
}
