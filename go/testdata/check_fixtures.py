"""Check that freshly written fixtures match the checked-in ones (go.yml runs this).

    python go/testdata/check_fixtures.py go/testdata/fixtures NEWDIR

Both directories must hold the same *.parquet files, and every file must be
byte-identical, except demo.parquet and units.parquet: NumPy's float functions
(arcsin, sin, powers) can change the last bit of their values on another CPU (GitHub's
runners), so those two are compared by layout and value instead:

- the Parquet layout: writer, format version, schema leaves, row groups and their row
  counts, and for each column chunk its codec, encodings, value and null counts, and
  whether it has a dictionary page, a column index, an offset index and a bloom
  filter. Byte offsets and sizes are left out (other float bits compress to
  other sizes), and so is the number of data pages per chunk, which pyarrow's metadata
  doesn't expose (it is in the offset index, which pyarrow doesn't read); the writer
  splits pages by row count and size limits that last bits don't move;
- the same Arrow schema and metadata, the same nulls, NaN only where NaN, and the same
  values, floats to a relative 1e-12.
"""
from __future__ import annotations

import filecmp
import glob
import os
import sys

import numpy as np
import pyarrow as pa
import pyarrow.compute as pc
import pyarrow.parquet as pq

#: the fixtures with values from NumPy's transcendental functions
BY_VALUE = {"demo.parquet", "units.parquet"}


def layout(path):
    md = pq.ParquetFile(path).metadata
    groups = []
    for i in range(md.num_row_groups):
        rg = md.row_group(i)
        cols = []
        for j in range(rg.num_columns):
            c = rg.column(j)
            st = c.statistics
            cols.append((c.path_in_schema, c.physical_type, c.compression, tuple(sorted(c.encodings)), c.num_values,
                         st.null_count if st is not None and st.has_null_count else None,
                         c.has_dictionary_page, c.has_column_index, c.has_offset_index,
                         c.bloom_filter_offset is not None))
        groups.append((rg.num_rows, tuple(cols)))
    sch = md.schema
    leaves = tuple((c.path, c.physical_type, str(c.logical_type), c.max_definition_level, c.max_repetition_level)
                   for c in (sch.column(i) for i in range(len(sch))))
    return md.created_by, md.format_version, sch.to_arrow_schema(), leaves, md.num_rows, tuple(groups)


def same_values(a: pa.ChunkedArray, b: pa.ChunkedArray) -> bool:
    if not a.is_null().equals(b.is_null()):
        return False
    if not pa.types.is_floating(a.type):
        return a.equals(b)
    keep = pc.invert(a.is_null())
    x = a.filter(keep).to_numpy(zero_copy_only=False)
    y = b.filter(keep).to_numpy(zero_copy_only=False)
    if not np.array_equal(np.isnan(x), np.isnan(y)):
        return False
    m = ~np.isnan(x)
    return bool(np.allclose(x[m], y[m], rtol=1e-12, atol=0))


def main(ref: str, new: str) -> int:
    bad = 0
    files = sorted(glob.glob(os.path.join(ref, "*.parquet")))
    if not files:
        print(f"no fixtures in {ref}")
        return 1
    known = {os.path.basename(f) for f in files}
    for g in sorted(glob.glob(os.path.join(new, "*.parquet"))):
        if os.path.basename(g) not in known:
            print(f"EXTRA {os.path.basename(g)} (written, but not checked in)")
            bad += 1
    for f in files:
        name = os.path.basename(f)
        g = os.path.join(new, name)
        if not os.path.exists(g):
            print(f"MISSING {name}")
            bad += 1
        elif filecmp.cmp(f, g, shallow=False):
            print(f"identical {name}")
        elif name not in BY_VALUE:
            print(f"DIFFERS {name} (must be byte-identical)")
            bad += 1
        elif layout(f) != layout(g):
            print(f"DIFFERS {name}: Parquet layout")
            bad += 1
        else:
            a, b = pq.read_table(f), pq.read_table(g)
            if not a.schema.equals(b.schema, check_metadata=True):
                print(f"DIFFERS {name}: schema or metadata")
                bad += 1
                continue
            cols = [n for n, x, y in zip(a.column_names, a.columns, b.columns) if not same_values(x, y)]
            if cols:
                print(f"DIFFERS {name}: values in {cols}")
                bad += 1
            else:
                print(f"same layout and values {name}")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(*sys.argv[1:]))
