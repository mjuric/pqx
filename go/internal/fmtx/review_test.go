package fmtx

import (
	"math"
	"math/big"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/mjuric/pqx/go/internal/data"
)

// Tests from the review of WP3: the fixes, and cases mutation testing
// showed weren't pinned down.

func TestHugeAnglesDontPanic(t *testing.T) {
	for _, v := range []float64{1e303, -1e303, math.MaxFloat64, 5e302} {
		for _, plus := range []bool{true, false} {
			if s := DegToDMS(v, plus); s != "" && math.Abs(v)*360_000 > math.MaxFloat64 {
				t.Errorf("DegToDMS(%g) = %q", v, s)
			}
		}
		_ = Derived("lon", KindAngle, v, "deg")
		_ = Derived("dec", KindAngle, v, "deg")
		_ = DegToHMS(v)
	}
	eq(t, "1e303", DegToDMS(1e303, false), "")
}

func ts(y int, m time.Month, d, h, mi, s int) data.Timestamp {
	return data.Timestamp{T: time.Date(y, m, d, h, mi, s, 0, time.UTC)}
}

func TestStrftimeReviewCases(t *testing.T) {
	naive := ts(2026, 1, 2, 3, 4, 5)
	utc := naive
	utc.Zoned = true
	for _, v := range []data.Timestamp{naive, utc} {
		for _, f := range []string{"%-z", "%_z", "%10z", "%^z", "%Ez", "%Oz", "%0z"} {
			eq(t, f, fo(v, KindTime, specOv(f+"|")), "|") // glibc: tm_isdst is -1
		}
	}
	eq(t, "%E%", fo(naive, KindTime, specOv("%E%")), "%")
	eq(t, "%O%", fo(naive, KindTime, specOv("%O%")), "%")
	eq(t, "%5E%", fo(naive, KindTime, specOv("%5E%")), "    %")
	eq(t, "%ET", fo(naive, KindTime, specOv("%ET")), "03:04:05")
	eq(t, "%Er", fo(naive, KindTime, specOv("%Er")), "03:04:05 AM")
	eq(t, "%-e", fo(naive, KindTime, specOv("%-e")), "2")
	eq(t, "%e", fo(naive, KindTime, specOv("%e")), " 2")
	// a non-ASCII character after %: copied whole, padded without it
	for f, want := range map[string]string{"%é": "%é", "%10é": "       %10é", "%3漢x": " %3漢x", "%E漢": "%E漢"} {
		got := fo(naive, KindTime, specOv(f))
		eq(t, f, got, want)
		eq(t, f+" utf8", utf8.ValidString(got), true)
	}
	// output Python's buffer can't hold is "", and costs nothing
	start := time.Now()
	for range 100 {
		eq(t, "%9999999Y", fo(naive, KindTime, specOv("%9999999Y")), "")
		eq(t, "many", fo(naive, KindTime, specOv(strings.Repeat("%3000Y", 50))), "")
	}
	// (formatting it in full took about 11 ms per cell, over 2 s here; the
	// limit leaves room for busy CI runners and -race)
	if d := time.Since(start); d > time.Second {
		t.Errorf("huge widths took %v", d)
	}
	s := Format(naive, KindTime, Opts{Override: specOv("%2046Y|"), Unsafe: true})
	eq(t, "%2046Y| len", len(s), 2047) // 2047 + NUL fits a buffer of 2048
	eq(t, "%2047Y|", Format(naive, KindTime, Opts{Override: specOv("%2047Y|")}), "")
}

// %U and %W against their definitions: the Sundays (Mondays) so far.
func TestStrftimeWeekNumbers(t *testing.T) {
	for _, y := range []int{2021, 2023, 2024, 2026} { // 2023 starts on a Sunday
		sundays, mondays := 0, 0
		for d := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == y; d = d.AddDate(0, 0, 1) {
			if d.Weekday() == time.Sunday {
				sundays++
			}
			if d.Weekday() == time.Monday {
				mondays++
			}
			got := glibcStrftime(d, "%U %W")
			want := pad2(sundays) + " " + pad2(mondays)
			if got != want {
				t.Fatalf("%s: %%U %%W = %q, want %q", d.Format(time.DateOnly), got, want)
			}
		}
	}
}

func TestDecimalWidthStartingWithZero(t *testing.T) {
	d := data.Decimal{Unscaled: big.NewInt(-30), Scale: 2}
	for _, s := range []string{"00G", "-00.3G", " #00.3F", "00,", "000", "<05"} {
		if s == "<05" {
			if _, err := formatDecimal(d, s); err == nil {
				continue // fill and align, then a width
			}
		}
		if out, err := formatDecimal(d, s); err == nil {
			t.Errorf("format(Decimal, %q) = %q, want an error", s, out)
		}
	}
	for s, want := range map[string]string{"0": "-0.30", "05": "-0.30", "08.3f": "-000.300", "010,": "-00,000.30"} {
		out, err := formatDecimal(d, s)
		if err != nil || out != want {
			t.Errorf("format(Decimal, %q) = %q, %v; want %q", s, out, err, want)
		}
	}
}

func TestParseOverrideStripsLikePython(t *testing.T) {
	eq(t, "seps", ParseOverride("\x1c\x1d3\x1e\x1f"), digitsOv(3))
	eq(t, "nbsp", ParseOverride(" .2f "), specOv(".2f"))
	eq(t, "nel", ParseOverride("\u0085  4 "), digitsOv(4))
}

func TestOverrideTextIsSafe(t *testing.T) {
	for _, s := range []string{"\x1b]0;x\x07>12", "\u202e<30", "\u009b2J^5", "\x1b[2J%Y"} {
		o := specOv(s)
		if d := DescribeOverride(o, KindFloat); anyControls(d) || strings.ContainsRune(d, 0x202E) {
			t.Errorf("DescribeOverride(%q) = %q", s, d)
		}
		for _, k := range []Kind{"", KindFloat, KindTime, KindStr, KindBool} {
			if m := OverrideError(specOv(s+"q"), k, nil); anyControls(m) || strings.ContainsRune(m, 0x202E) {
				t.Errorf("OverrideError(%q, %q) = %q", s, k, m)
			}
		}
	}
	eq(t, "message", OverrideError(specOv("\x1b>12qq"), KindFloat, nil), "Invalid format specifier '␛>12qq' for object of type 'float'")
}

func TestLongStringsCutBeforeSanitizing(t *testing.T) {
	long := strings.Repeat("\x1b\u202e", 2_000_000)
	start := time.Now()
	got := fv(long, KindStr)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("took %v", d)
	}
	naive := Sanitize(long[:400], true)
	eq(t, "same", got, cutRunes(naive, 39)+"…")
	// every width and string: the same as sanitizing first
	rng := rand.New(rand.NewPCG(1, 2))
	parts := []string{"a", "\x1b", "\u202e", "\t", "\n", "日", "\xff", "\u009b"}
	for range 3000 {
		var b strings.Builder
		for range rng.IntN(30) {
			b.WriteString(parts[rng.IntN(len(parts))])
		}
		s, w := b.String(), 1+rng.IntN(20)
		want := Sanitize(s, true)
		if utf8.RuneCountInString(want) > w {
			want = cutRunes(want, w-1) + "…"
		}
		eq(t, strconv.Quote(s), Format(s, KindStr, Opts{Width: w}), want)
		want = Sanitize(s, true)
		if utf8.RuneCountInString(want) > w {
			want = cutRunes(want, w-1) + "…"
		}
		eq(t, strconv.Quote(s)+" spec", Format(s, KindStr, Opts{Width: w, Override: specOv("s")}), want)
	}
}

// floorLog10 at each threshold: the value itself reads k, the double below k-1.
func TestFloorLog10Thresholds(t *testing.T) {
	for k, v := range log10Up {
		eq(t, "at "+strconv.Itoa(k), floorLog10(v), k)
		eq(t, "below "+strconv.Itoa(k), floorLog10(math.Nextafter(v, 0)), k-1)
	}
	for k := -3; k <= 8; k++ {
		p, _ := strconv.ParseFloat("1e"+strconv.Itoa(k), 64)
		eq(t, "power "+strconv.Itoa(k), floorLog10(p), k)
	}
}

// fmtFloat with 16 and 17 digits rounds twice, as Python's round() then
// format do.
func TestFmtFloatDoubleRounding(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	differ := 0
	for range 200_000 {
		v := math.Pow(10, rng.Float64()*12-3) * (1 + rng.Float64())
		if v >= 1e9 {
			continue
		}
		for _, sig := range []int{16, 17} {
			mag := floorLog10(v)
			dec := max(0, sig-1-mag)
			if dec == 0 {
				continue
			}
			once := strconv.FormatFloat(v, 'f', dec, 64)
			y, _ := strconv.ParseFloat(once, 64)
			twice := strconv.FormatFloat(y, 'f', dec, 64)
			if once != twice {
				differ++
			}
			want := strings.TrimRight(strings.TrimRight(twice, "0"), ".")
			if got := fmtFloat(v, sig); got != want {
				t.Fatalf("fmtFloat(%v, %d) = %q, want %q", v, sig, got, want)
			}
		}
	}
	// Rounding twice made no difference in 4 million values tried in Python
	// either: the second rounding is kept to follow Python, not because a
	// value is known to need it.
	t.Logf("%d values where rounding twice differs", differ)
}

func TestListCountSuffix(t *testing.T) {
	l := data.List{int64(1), int64(2), int64(3)}
	eq(t, "3", fv(l, KindNested), "[1, 2, 3]")
	eq(t, "4", fv(append(l, int64(4)), KindNested), "[1, 2, 3, 4] (4)")
}

func TestHMSRoundsUpTheSeconds(t *testing.T) {
	// 0h 59m 59.99955s: the seconds would print as 60.000
	eq(t, "59.9995", DegToHMS(59.9999925/4), "01h00m00.000s")
	eq(t, "59.9994", DegToHMS(59.9999900/4), "00h59m59.999s")
}

func TestDecAtThePoles(t *testing.T) {
	eq(t, "-90", Derived("dec", KindAngle, -90.0, ""), "-90°00′00.00″")
	eq(t, "90", Derived("dec", KindAngle, 90.0, ""), "+90°00′00.00″")
	eq(t, "below -90", Derived("dec", KindAngle, -90.000001, ""), "")
	eq(t, "above 90", Derived("dec", KindAngle, 90.000001, ""), "")
}

// timedelta(days=…) rounds a half microsecond to even, on the total.
func TestMJDHalfMicrosecond(t *testing.T) {
	check := func(mjd float64, want int64) {
		t.Helper()
		got, ok := mjdMicros(mjd)
		if !ok || got != want {
			t.Errorf("mjdMicros(%v) = %d, want %d", mjd, got, want)
		}
	}
	check(3.0/16384, 15820312)                    // 15820312.5: down to even
	check(1+1.0/16384, 86_400_000_000+5273438)    // 5273437.5 on an odd total: up
	check(-3.0/16384, -15820312)                  // -15820312.5: to even
	check(2+5.0/16384, 2*86_400_000_000+26367188) // 26367187.5, odd: up
	check(0.0, 0)
}

func TestKindForNamesEndingInNewline(t *testing.T) {
	// Python's $ matches before a final newline
	eq(t, "ra\\n", KindFor("ra\n", f64, ""), KindAngle)
	eq(t, "x_tai\\n", KindFor("x_tai\n", f64, ""), KindMJD)
	eq(t, "xmag", KindFor("xmagx", f64, ""), KindMag)
	eq(t, "ra\\n\\n", KindFor("ra\n\n", f64, ""), KindFloat)
}

func TestShortTypeSanitizesTheTimeZone(t *testing.T) {
	ty := &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "\x1b]0;x\x07\u202e"}
	s := ShortType(ty)
	if anyControls(s) || strings.ContainsRune(s, 0x202E) {
		t.Errorf("ShortType = %q", s)
	}
	d := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int32, ValueType: ty}
	if s := ShortType(d); anyControls(s) {
		t.Errorf("ShortType(dict) = %q", s)
	}
}

// DegToHMS's products are rounded before the next subtraction, as in
// Python: had arm64's fused multiply-subtract been used (math.FMA here),
// 0.5° would read 00h02m-0.000s. The text never has a negative field.
func TestHMSIsRobustToFusedArithmetic(t *testing.T) {
	if fused := math.FMA(0.5/15, 60, -2) * 60; fused >= 0 {
		t.Errorf("fused seconds %g: the test no longer shows the hazard", fused)
	}
	eq(t, "0.5", DegToHMS(0.5), "00h02m00.000s")
	rng := rand.New(rand.NewPCG(5, 6))
	for range 100_000 {
		v := rng.Float64() * 360
		if s := DegToHMS(v); strings.Contains(s, "-") {
			t.Fatalf("DegToHMS(%v) = %q", v, s)
		}
	}
}
