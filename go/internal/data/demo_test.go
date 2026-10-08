package data

import (
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// demo is an LSST-like file in the spirit of pqx's demo fixture (20,000
// rows in 2,500-row row groups), with its values kept as truth.
type demo struct {
	path    string
	n       int
	band    []string
	mag     []float64 // NaN for NULL (magNull)
	magNull []bool
	flux    []float64 // with NaN and +inf
	ingest  []time.Time
	ra, dec []float64
	snr     []float64
}

const (
	demoRows  = 20_000
	demoGroup = 2_500
)

var demoEpoch = time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

func writeDemo(t testing.TB) *demo {
	t.Helper()
	d := &demo{path: filepath.Join(t.TempDir(), "demo.parquet"), n: demoRows}
	rng := rand.New(rand.NewPCG(7, 7))
	bands := []string{"u", "g", "r", "i", "z", "y"}
	md := func(kv ...string) arrow.Metadata {
		var k, v []string
		for i := 0; i+1 < len(kv); i += 2 {
			k, v = append(k, kv[i]), append(v, kv[i+1])
		}
		return arrow.NewMetadata(k, v)
	}
	sc := arrow.NewSchema([]arrow.Field{
		{Name: "diaSourceId", Type: arrow.PrimitiveTypes.Int64, Metadata: md("description", "Unique identifier.")},
		{Name: "band", Type: arrow.BinaryTypes.String, Metadata: md("doc", "Filter band.")},
		{Name: "mag", Type: arrow.PrimitiveTypes.Float64, Nullable: true, Metadata: md("units", "mag", "description", "PSF magnitude.")},
		{Name: "psfFlux", Type: arrow.PrimitiveTypes.Float64, Nullable: true, Metadata: md("unit", "nJy", "description", "[ignored] Flux.")},
		{Name: "ingestTime", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}},
		{Name: "ra", Type: arrow.PrimitiveTypes.Float64, Metadata: md("unit", "deg", "description", "Right ascension.")},
		{Name: "dec", Type: arrow.PrimitiveTypes.Float64, Metadata: md("description", "[deg] Declination.")},
		{Name: "snr", Type: arrow.PrimitiveTypes.Float64},
	}, func() *arrow.Metadata { m := md("table", "Demo"); return &m }())
	b := array.NewRecordBuilder(memory.DefaultAllocator, sc)
	defer b.Release()
	for i := range demoRows {
		band := bands[rng.IntN(6)]
		mag := 15 + 10*rng.Float64()
		magNull := i%37 == 5
		flux := rng.NormFloat64()*1000 + 300
		switch {
		case i%101 == 7:
			flux = math.NaN()
		case i == 5:
			flux = math.Inf(1)
		}
		ingest := demoEpoch.Add(time.Duration(i) * 1500 * time.Millisecond)
		ra := 360 * rng.Float64()
		dec := math.Asin(2*rng.Float64()-1) * 180 / math.Pi
		switch i {
		case 10:
			ra = -10.5 // wraps to 349.5
		case 11:
			ra = 725 // wraps to 5
		case 12:
			dec = 95 // off the sky
		case 13:
			dec = math.NaN()
		case 14:
			ra, dec = 360, 90 // on the grid's far edges
		}
		snr := math.Abs(rng.NormFloat64()*20 + 30)
		d.band = append(d.band, band)
		d.mag = append(d.mag, map[bool]float64{true: math.NaN(), false: mag}[magNull])
		d.magNull = append(d.magNull, magNull)
		d.flux = append(d.flux, flux)
		d.ingest = append(d.ingest, ingest)
		d.ra = append(d.ra, ra)
		d.dec = append(d.dec, dec)
		d.snr = append(d.snr, snr)

		b.Field(0).(*array.Int64Builder).Append(int64(i))
		b.Field(1).(*array.StringBuilder).Append(band)
		if magNull {
			b.Field(2).AppendNull()
		} else {
			b.Field(2).(*array.Float64Builder).Append(mag)
		}
		b.Field(3).(*array.Float64Builder).Append(flux)
		b.Field(4).(*array.TimestampBuilder).Append(arrow.Timestamp(ingest.UnixMicro()))
		b.Field(5).(*array.Float64Builder).Append(ra)
		b.Field(6).(*array.Float64Builder).Append(dec)
		b.Field(7).(*array.Float64Builder).Append(snr)
	}
	rec := b.NewRecordBatch()
	defer rec.Release()
	tbl := array.NewTableFromRecords(sc, []arrow.RecordBatch{rec})
	defer tbl.Release()
	writeTable(t, d.path, tbl, demoGroup, 64<<10)
	return d
}

// demoDataset opens a demo file.
func demoDataset(t testing.TB) (*demo, *dataset) {
	t.Helper()
	dm := writeDemo(t)
	ds, err := Open(dm.path, Options{Threads: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ds.Close() })
	return dm, ds.(*dataset)
}
