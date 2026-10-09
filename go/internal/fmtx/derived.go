package fmtx

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/mjuric/pqx/go/internal/data"
)

// mjdEpoch is MJD 0, 1858-11-17 UTC.
var mjdEpoch = time.Date(1858, 11, 17, 0, 0, 0, 0, time.UTC)

// MJD limits of Python's datetime (years 1 to 9999), with room to spare:
// beyond them the result can't be a date.
const mjdMin, mjdMax = -700_000, 3_000_000

func mjdToISO(mjd float64) string {
	us, ok := mjdMicros(mjd)
	if !ok {
		return ""
	}
	days := floorDiv(us, 86_400_000_000)
	t := mjdEpoch.AddDate(0, 0, int(days)).Add(time.Duration(us-days*86_400_000_000) * time.Microsecond)
	if t.Year() < 1 || t.Year() > 9999 {
		return ""
	}
	return glibcStrftime(t, "%Y-%m-%d %H:%M:%S.") + fmt.Sprintf("%03d", t.Nanosecond()/1e6)
}

// mjdMicros is timedelta(days=mjd) in microseconds, as CPython's
// delta_new builds it: whole days exactly, the fraction in microseconds
// rounded half to even (on the total so far).
func mjdMicros(mjd float64) (int64, bool) {
	if math.IsNaN(mjd) || math.IsInf(mjd, 0) {
		return 0, false
	}
	ip, fp := math.Modf(mjd)
	if ip < mjdMin || ip > mjdMax {
		return 0, false
	}
	us := int64(ip) * 86_400_000_000
	if fp != 0 {
		ip2, left := math.Modf(86_400_000_000 * fp)
		us += int64(ip2)
		whole := math.Round(left)
		if math.Abs(whole-left) == 0.5 {
			odd := float64(us & 1)
			whole = 2*math.Round((left+odd)*0.5) - odd
		}
		us += int64(whole)
	}
	return us, true
}

// pyMod is Python's float %: the remainder has the divisor's sign.
func pyMod(x, y float64) float64 {
	m := math.Mod(x, y)
	if m != 0 {
		if (y < 0) != (m < 0) {
			m += y
		}
	} else {
		m = math.Copysign(0, y)
	}
	return m
}

func degToHMS(deg float64) string {
	h := pyMod(deg, 360) / 15
	if math.IsNaN(h) {
		return ""
	}
	hh := int(h)
	m := (h - float64(hh)) * 60
	mm := int(m)
	ss := (m - float64(mm)) * 60
	if ss >= 59.9995 {
		ss, mm = 0, mm+1
	}
	if mm >= 60 {
		mm, hh = 0, (hh+1)%24
	}
	return pad2(hh) + "h" + pad2(mm) + "m" + pyFloatString6(ss) + "s"
}

// pyFloatString6 is f"{ss:06.3f}".
func pyFloatString6(ss float64) string {
	s, _ := formatFloat(ss, "06.3f")
	return s
}

func pad2(n int) string {
	if n >= 0 && n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func degToDMS(deg float64, plus bool) string {
	if math.IsNaN(deg) || math.IsInf(deg, 0) {
		return ""
	}
	// round(abs(deg) * 360_000): hundredths of an arcsecond, half to even
	csf := math.RoundToEven(math.Abs(deg) * 360_000)
	if math.IsInf(csf, 0) {
		return "" // (Python raises OverflowError)
	}
	cs, _ := new(big.Float).SetFloat64(csf).Int(nil)
	full := big.NewInt(360 * 360_000)
	if !plus && 0 <= deg && deg < 360 && cs.Cmp(full) == 0 {
		cs.SetInt64(0)
	}
	sign := ""
	switch {
	case deg < 0 && cs.Sign() != 0:
		sign = "-"
	case plus:
		sign = "+"
	}
	d, rest := new(big.Int).DivMod(cs, big.NewInt(360_000), new(big.Int))
	r := rest.Int64()
	mm, ss := r/6000, r%6000
	ds := d.Text(10)
	if len(ds) < 2 {
		ds = "0" + ds
	}
	return sign + ds + "°" + pad2(int(mm)) + "′" + pad2(int(ss/100)) + "." + pad2(int(ss%100)) + "″"
}

// number is v as a float if it is an int or a float (not a bool).
func number(v data.Value) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	return 0, false
}

func derived(name string, k Kind, v data.Value, unit string) string {
	f, ok := number(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	low := strings.ToLower(name)
	switch k {
	case KindMJD:
		if strings.Contains(low, "jd") && !strings.Contains(low, "mjd") && f > 2_400_000 {
			return mjdToISO(f-2_400_000.5) + " (from JD)"
		}
		if 0 < f && f < 200_000 {
			return mjdToISO(f)
		}
	case KindAngle:
		if reLat.MatchString(name) || reLatCamel.MatchString(name) || reVizierDE.MatchString(name) {
			if -90 <= f && f <= 90 {
				return degToDMS(f, true)
			}
			return ""
		}
		if reRA.MatchString(name) || reRACamel.MatchString(name) || reVizierRA.MatchString(name) {
			return degToHMS(f)
		}
		if math.Abs(f) > 360 && !isDegUnit(normUnit(unit)) {
			return ""
		}
		return degToDMS(f, false)
	case KindErr:
		if (strings.Contains(low, "ra") || strings.Contains(low, "dec")) && math.Abs(f) < 1 {
			return pyFloatString(f*3.6e6, 'g', 3, false, false, false) + " mas"
		}
	case KindFlux:
		if f > 0 && !strings.Contains(low, "err") {
			return pyFloatString(-2.5*math.Log10(f)+31.4, 'f', 3, false, false, false) + " AB mag (if nJy)"
		}
	}
	return ""
}
