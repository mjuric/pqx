#!/bin/bash
# Esc test: apply a slow filter (counting takes ~7 s on SSSource), wait for the count
# spinner, press Esc, and time until the count shows as cancelled. Step 2 is the time
# to the spinner, step 4 the time from Esc to "cancelled".
#   PQX_PY=... PQX_GO=... PTY_PYTHON=... esc.sh SSSource.parquet
HERE=$(cd "$(dirname "$0")" && pwd)
F=${1:?usage: esc.sh SSSource.parquet}
W="levenshtein(repeat(obsid,4), repeat(trksub,4)) > 5"
for app in "${PQX_GO:-$HERE/../../bin/pqx}" "${PQX_PY:-pqx}"; do
  for r in 1 2 3; do
    echo -n "$app "
    "${PTY_PYTHON:-python3}" "$HERE/ptytime.py" --timeout 30 -- "$app" "$F" ::: '=>Ltt1V02R' ::: '/=>SLEEP:0.5' \
      ::: "$W\r=>(?i)counting" ::: '=>SLEEP:1.0' ::: '\x1b=>(count cancelled|✓ rows\s+·)' 2>&1 | tail -1
  done
done
