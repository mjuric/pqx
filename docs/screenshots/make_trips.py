"""Write a synthetic (made-up) taxi-trip Parquet file for the README screenshots.

Usage: python make_trips.py OUT.parquet ROWS
"""
import sys, numpy as np, pyarrow as pa, pyarrow.parquet as pq
out, n = sys.argv[1], int(sys.argv[2])
rng = np.random.default_rng(7)
zones = ["Midtown Center", "Upper East Side South", "JFK Airport", "LaGuardia Airport", "Times Sq/Theatre District",
         "East Village", "Lincoln Square East", "Murray Hill", "Penn Station/Madison Sq West", "Clinton East",
         "Union Sq", "Gramercy", "West Village", "Upper West Side North", "Financial District North", "Williamsburg",
         "Astoria", "Park Slope", "Harlem", "Chelsea"]
zw = rng.dirichlet(np.ones(len(zones)) * 0.6)
t0 = np.datetime64("2024-03-01T00:00:00", "us").astype("int64")
pick = np.sort(t0 + rng.integers(0, 31 * 86400, n) * 10**6)
dist = np.round(rng.lognormal(0.6, 0.75, n), 2)
dur = (dist * rng.uniform(2.5, 6.0, n) * 60 * 10**6 + rng.integers(60, 600, n) * 10**6).astype("int64")
fare = np.round(3.0 + dist * 2.5 + dur / 60e6 * 0.5, 2)
pay = rng.choice(["card", "cash", "no charge", "dispute"], n, p=[0.78, 0.19, 0.02, 0.01])
tip = np.where(pay == "card", np.round(fare * rng.choice([0, .15, .2, .25, .3], n, p=[.1, .2, .4, .2, .1]), 2), 0.0)
tolls = np.where(rng.random(n) < 0.06, 6.94, 0.0)
# pickup locations: a few hotspots around a city centre
cx, cy = -73.98, 40.75
hot = np.array([[-73.985, 40.758], [-73.97, 40.775], [-73.99, 40.73], [-73.78, 40.645], [-73.87, 40.775],
                [-73.955, 40.715], [-74.01, 40.71], [-73.95, 40.80]])
h = rng.integers(0, len(hot), n)
spread = np.where(h == 3, 0.008, np.where(h == 4, 0.006, 0.018))
lon = np.round(hot[h, 0] + rng.normal(0, 1, n) * spread, 6)
lat = np.round(hot[h, 1] + rng.normal(0, 1, n) * spread * 0.75, 6)
pz = rng.choice(len(zones), n, p=zw)
dz = rng.choice(len(zones), n, p=zw)
pax = rng.choice([1, 2, 3, 4, 5, 6], n, p=[.7, .15, .06, .04, .03, .02]).astype("int8")
pax_arr = pa.array(pax, mask=rng.random(n) < 0.03)
def f(name, typ, unit=None, desc=None):
    md = {}
    if unit: md[b"unit"] = unit.encode()
    if desc: md[b"description"] = desc.encode()
    return pa.field(name, typ, metadata=md or None)
schema = pa.schema([
    f("trip_id", pa.int64(), desc="Unique trip identifier"),
    f("vendor", pa.dictionary(pa.int8(), pa.string()), desc="Taxi technology provider"),
    f("pickup_time", pa.timestamp("s"), desc="When the meter was engaged"),
    f("dropoff_time", pa.timestamp("s"), desc="When the meter was disengaged"),
    f("passenger_count", pa.int8(), desc="Driver-entered number of passengers"),
    f("trip_distance", pa.float32(), unit="mi", desc="Distance reported by the taximeter"),
    f("pickup_zone", pa.dictionary(pa.int16(), pa.string()), desc="Taxi zone where the trip started"),
    f("dropoff_zone", pa.dictionary(pa.int16(), pa.string()), desc="Taxi zone where the trip ended"),
    f("pickup_lon", pa.float64(), desc="Pickup longitude"),
    f("pickup_lat", pa.float64(), desc="Pickup latitude"),
    f("payment_type", pa.dictionary(pa.int8(), pa.string()), desc="How the passenger paid"),
    f("fare_amount", pa.decimal128(9, 2), unit="USD", desc="Time-and-distance fare"),
    f("tip_amount", pa.decimal128(9, 2), unit="USD", desc="Tip (card payments only)"),
    f("tolls_amount", pa.decimal128(9, 2), unit="USD"),
    f("total_amount", pa.decimal128(9, 2), unit="USD", desc="Total charged to the passenger"),
    f("store_and_fwd", pa.bool_(), desc="Trip record held in vehicle memory before sending"),
])
def dec(x): return pa.array(x, pa.float64()).cast(pa.decimal128(9, 2))
tbl = pa.table([
    pa.array(np.arange(n, dtype="int64") + 1_000_000_000),
    pa.array(rng.choice(["Creative Mobile", "VeriFone", "Curb"], n, p=[.35, .6, .05])).dictionary_encode().cast(pa.dictionary(pa.int8(), pa.string())),
    pa.array(pick // 10**6, pa.timestamp("s")), pa.array((pick + dur) // 10**6, pa.timestamp("s")),
    pax_arr, pa.array(dist.astype("float32")),
    pa.DictionaryArray.from_arrays(pa.array(pz.astype("int16")), pa.array(zones)),
    pa.DictionaryArray.from_arrays(pa.array(dz.astype("int16")), pa.array(zones)),
    pa.array(lon), pa.array(lat),
    pa.array(pay).dictionary_encode().cast(pa.dictionary(pa.int8(), pa.string())),
    dec(fare), dec(tip), dec(tolls), dec(np.round(fare + tip + tolls + 1.0, 2)),
    pa.array(rng.random(n) < 0.004),
], schema=schema)
pq.write_table(tbl, out, row_group_size=250_000, compression="zstd")
print(out, tbl.num_rows, tbl.num_columns)
