// Package indicators implements technical indicators as pure functions.
// Slices are oldest-first; positions that cannot be computed are NaN.
package indicators

import "math"

// N is the "not available" marker.
func N() float64 { return math.NaN() }

func isNA(v float64) bool { return math.IsNaN(v) }

func fill(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

// SMA – simple moving average.
func SMA(v []float64, period int) []float64 {
	out := fill(len(v))
	if period <= 0 {
		return out
	}
	sum := 0.0
	for i := range v {
		sum += v[i]
		if i >= period {
			sum -= v[i-period]
		}
		if i >= period-1 {
			out[i] = sum / float64(period)
		}
	}
	return out
}

// EMA – exponential moving average, seeded with the first SMA.
func EMA(v []float64, period int) []float64 {
	out := fill(len(v))
	if period <= 0 {
		return out
	}
	k := 2.0 / float64(period+1)
	seeded := false
	prev := 0.0
	for i := range v {
		if !seeded {
			if i >= period-1 {
				s := 0.0
				for j := i - period + 1; j <= i; j++ {
					s += v[j]
				}
				prev = s / float64(period)
				out[i] = prev
				seeded = true
			}
			continue
		}
		prev = v[i]*k + prev*(1-k)
		out[i] = prev
	}
	return out
}

// MACDResult bundles the three MACD series.
type MACDResult struct {
	MACD   []float64 `json:"macd"`
	Signal []float64 `json:"signal"`
	Hist   []float64 `json:"hist"`
}

// MACD – 12/26/9 by default.
func MACD(v []float64, fast, slow, signalPeriod int) MACDResult {
	ef := EMA(v, fast)
	es := EMA(v, slow)
	line := fill(len(v))
	for i := range v {
		if !isNA(ef[i]) && !isNA(es[i]) {
			line[i] = ef[i] - es[i]
		}
	}
	filled := make([]float64, len(line))
	for i, x := range line {
		if isNA(x) {
			filled[i] = 0
		} else {
			filled[i] = x
		}
	}
	sig := EMA(filled, signalPeriod)
	hist := fill(len(v))
	for i := range v {
		if isNA(line[i]) {
			sig[i] = math.NaN()
			continue
		}
		if !isNA(sig[i]) {
			hist[i] = line[i] - sig[i]
		}
	}
	return MACDResult{MACD: line, Signal: sig, Hist: hist}
}

// RSI – Wilder smoothing.
func RSI(v []float64, period int) []float64 {
	out := fill(len(v))
	if len(v) <= period {
		return out
	}
	gain, loss := 0.0, 0.0
	for i := 1; i <= period; i++ {
		d := v[i] - v[i-1]
		if d >= 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	ag, al := gain/float64(period), loss/float64(period)
	if al == 0 {
		out[period] = 100
	} else {
		out[period] = 100 - 100/(1+ag/al)
	}
	for i := period + 1; i < len(v); i++ {
		d := v[i] - v[i-1]
		g, l := 0.0, 0.0
		if d >= 0 {
			g = d
		} else {
			l = -d
		}
		ag = (ag*float64(period-1) + g) / float64(period)
		al = (al*float64(period-1) + l) / float64(period)
		if al == 0 {
			out[i] = 100
		} else {
			out[i] = 100 - 100/(1+ag/al)
		}
	}
	return out
}

// KDJResult bundles K/D/J.
type KDJResult struct {
	K []float64 `json:"k"`
	D []float64 `json:"d"`
	J []float64 `json:"j"`
}

// KDJ – classic 9,3,3 stochastic.
func KDJ(highs, lows, closes []float64, n int) KDJResult {
	k := fill(len(closes))
	d := fill(len(closes))
	j := fill(len(closes))
	pk, pd := 50.0, 50.0
	for i := range closes {
		if i < n-1 {
			continue
		}
		hh, ll := math.Inf(-1), math.Inf(1)
		for x := i - n + 1; x <= i; x++ {
			hh = math.Max(hh, highs[x])
			ll = math.Min(ll, lows[x])
		}
		rsv := 50.0
		if hh != ll {
			rsv = (closes[i] - ll) / (hh - ll) * 100
		}
		kk := 2.0/3.0*pk + 1.0/3.0*rsv
		dd := 2.0/3.0*pd + 1.0/3.0*kk
		k[i], d[i], j[i] = kk, dd, 3*kk-2*dd
		pk, pd = kk, dd
	}
	return KDJResult{K: k, D: d, J: j}
}

// BollResult bundles the Bollinger bands.
type BollResult struct {
	Mid   []float64 `json:"mid"`
	Upper []float64 `json:"upper"`
	Lower []float64 `json:"lower"`
}

// Boll – Bollinger bands (period, mult).
func Boll(v []float64, period int, mult float64) BollResult {
	mid := SMA(v, period)
	up, lo := fill(len(v)), fill(len(v))
	for i := period - 1; i < len(v); i++ {
		if i < 0 || isNA(mid[i]) {
			continue
		}
		s := 0.0
		for j := i - period + 1; j <= i; j++ {
			s += (v[j] - mid[i]) * (v[j] - mid[i])
		}
		sd := math.Sqrt(s / float64(period))
		up[i] = mid[i] + mult*sd
		lo[i] = mid[i] - mult*sd
	}
	return BollResult{Mid: mid, Upper: up, Lower: lo}
}

// StdDev – population standard deviation.
func StdDev(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	m := 0.0
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	s := 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(v)))
}

// AnnualizedReturn – geometric annualisation assuming 252 trading days.
func AnnualizedReturn(closes []float64) float64 {
	if len(closes) < 2 || closes[0] <= 0 {
		return math.NaN()
	}
	years := float64(len(closes)-1) / 252.0
	if years <= 0 {
		return math.NaN()
	}
	return math.Pow(closes[len(closes)-1]/closes[0], 1/years) - 1
}

// MaxDrawdown – returns a negative number (or 0).
func MaxDrawdown(series []float64) float64 {
	peak := math.Inf(-1)
	maxDD := 0.0
	for _, v := range series {
		if v > peak {
			peak = v
		}
		if peak > 0 {
			if dd := (v - peak) / peak; dd < maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

// Sharpe – annualised Sharpe ratio with rf = 0.
func Sharpe(daily []float64, periodsPerYear float64) float64 {
	if len(daily) < 2 {
		return math.NaN()
	}
	m := 0.0
	for _, x := range daily {
		m += x
	}
	m /= float64(len(daily))
	sd := StdDev(daily)
	if sd == 0 {
		return math.NaN()
	}
	return m / sd * math.Sqrt(periodsPerYear)
}

// DailyReturns converts a close series into simple returns.
func DailyReturns(closes []float64) []float64 {
	out := make([]float64, 0, len(closes))
	for i := 1; i < len(closes); i++ {
		if closes[i-1] == 0 {
			continue
		}
		out = append(out, (closes[i]-closes[i-1])/closes[i-1])
	}
	return out
}

// LastValid returns the last non-NaN element, or NaN.
func LastValid(v []float64) float64 {
	for i := len(v) - 1; i >= 0; i-- {
		if !isNA(v[i]) {
			return v[i]
		}
	}
	return math.NaN()
}
