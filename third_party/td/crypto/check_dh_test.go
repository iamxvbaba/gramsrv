package crypto

import (
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkCheckDHVerifiedCacheHit(b *testing.B) {
	var cache validatedDHParameterCache
	require.NoError(b, checkDHWithCache(&cache, checkGPg, checkGPdhPrime))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = checkDHWithCache(&cache, checkGPg, checkGPdhPrime)
	}
}

func BenchmarkCheckDHUncached(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var cache validatedDHParameterCache
		if err := checkDHWithCache(&cache, checkGPg, checkGPdhPrime); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCheckDH(t *testing.T) {
	t.Run("OK", func(t *testing.T) {
		a := require.New(t)
		a.NoError(CheckDH(checkGPg, checkGPdhPrime))
	})
	t.Run("WrongG", func(t *testing.T) {
		require.Error(t, CheckDH(1337, checkGPdhPrime))
	})
	t.Run("TooSmallBits", func(t *testing.T) {
		require.Error(t, CheckDH(3, big.NewInt(4)))
	})
	t.Run("NilPrime", func(t *testing.T) {
		require.Error(t, CheckDH(3, nil))
	})
}

func TestValidatedDHParameterCacheDeduplicatesConcurrentSuccess(t *testing.T) {
	var cache validatedDHParameterCache
	key, ok := validatedDHKey(checkGPg, checkGPdhPrime)
	require.True(t, ok)

	const workers = 64
	start := make(chan struct{})
	var calls atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			require.NoError(t, cache.validate(key, func() error {
				calls.Add(1)
				return nil
			}))
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int64(1), calls.Load())
}

func TestValidatedDHParameterCacheDoesNotCacheFailures(t *testing.T) {
	var cache validatedDHParameterCache
	key, ok := validatedDHKey(checkGPg, checkGPdhPrime)
	require.True(t, ok)

	want := errors.New("invalid dh parameters")
	var calls int
	for i := 0; i < 2; i++ {
		err := cache.validate(key, func() error {
			calls++
			return want
		})
		require.ErrorIs(t, err, want)
	}
	require.Equal(t, 2, calls)
}

func Test_checkPrime(t *testing.T) {
	t.Run("OK", func(t *testing.T) {
		require.NoError(t, checkPrime(big.NewInt(5)))
	})
	t.Run("PNotPrime", func(t *testing.T) {
		require.Error(t, checkPrime(big.NewInt(4)))
	})
	t.Run("HalfPMinusOneNotPrime", func(t *testing.T) {
		require.Error(t, checkPrime(big.NewInt(13)))
	})
}
