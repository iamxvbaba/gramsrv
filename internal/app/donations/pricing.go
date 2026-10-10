package donations

import "math/big"

// usdPerStar mirrors the real Telegram Stars rate this server already uses
// elsewhere (internal/compat/tdesktop/startup_stubs.go's UsdRate: 0.013) --
// $0.013 per Star, i.e. ~76.9 Stars per US dollar.
const usdPerStarMicros = 13000 // $0.013 * 1e6

// microsPerWhole scales a "USD per one whole unit" rate (native currency or
// a stablecoin) by 1e6, matching DonationChain.ManualUSDRateMicros and
// DonationToken pricing throughout this package.
const microsPerWhole = 1_000_000

// usdMicrosForAmount converts a raw on-chain amount (smallest unit, e.g.
// wei) into USD, scaled by 1e6, given the asset's decimals and its price in
// USD-per-whole-unit (also scaled by 1e6). Pure integer math throughout --
// no float ever touches a monetary amount.
func usdMicrosForAmount(amountRaw *big.Int, decimals int, usdRateMicros int64) int64 {
	if amountRaw == nil || amountRaw.Sign() <= 0 || decimals < 0 || usdRateMicros <= 0 {
		return 0
	}
	// usdMicros = amountRaw * usdRateMicros / 10^decimals
	num := new(big.Int).Mul(amountRaw, big.NewInt(usdRateMicros))
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	result := new(big.Int).Quo(num, den)
	if !result.IsInt64() {
		return 0 // absurdly large deposit; refuse to overflow rather than mis-credit.
	}
	return result.Int64()
}

// starsForUSDMicros converts a priced deposit into Stars at the fixed
// usdPerStarMicros rate, rounding down -- a donor is never credited more
// Stars than their deposit was actually worth.
func starsForUSDMicros(usdMicros int64) int64 {
	if usdMicros <= 0 {
		return 0
	}
	return usdMicros / usdPerStarMicros
}
