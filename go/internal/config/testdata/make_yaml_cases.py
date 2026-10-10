"""Write yaml_cases.json: Python pqx's config.load_formats on ~4,000 formats.yaml documents, and
PyYAML's text for random column formats, for the Go port's differential test (yaml_test.go).

    cd <repo root>
    PYTHONPATH=$PWD <python with pqx's dependencies> go/internal/config/testdata/make_yaml_cases.py

Templates and values after the WP3 review's fuzz_yaml.py. Seeded: a second run writes the same
bytes.
"""
import json
import os
import random
import tempfile
from pathlib import Path

import yaml

from pqx import config as C

HERE = os.path.dirname(os.path.abspath(__file__))
rng = random.Random(11)

CH = list("ab:#-?'\" \t{}[],&*!|>%@`~=<.0123456789xXeE+_é日😀\\") + ["\x1b", "\xa0", "\u202e", "\x9b"]


def rname(maxlen=12):
    if rng.randrange(6) == 0:
        return rng.choice(["yes", "No", "on", "OFF", "null", "~", "", "1", "017", "0x1F", "0b11", "1_000", "1:30", ".5",
                           "1e3", "1.5e+3", ".inf", "-.Inf", ".NaN", "2026-01-02", "2026-1-2 3:04:05", "<<", "=", "-",
                           "---", "...", "?", ":", "- a", "? b", "a: b", "#c", "a #b", "true", "False", "Y", "n",
                           "0o17", "+1", "-0", "1:60", "190:20:30", "x" * 127, "y" * 128])
    return "".join(rng.choice(CH) for _ in range(rng.randint(0, maxlen)))


def rvalue():
    c = rng.randrange(5)
    if c == 0:
        return rng.randint(0, 17)
    if c == 1:
        return rng.choice([".2f", ".4f", ",d", ">12", "%Y-%m-%d", ".3e", "x", "#x", "+.3g", "_d", "^10", ".5", "<5",
                           "08", "=", "<<", "yes", "~"])
    return rng.choice([".2f", "%Y", "%H:%M"]) + rng.choice(["", " ", "#", ": ", "'", '"', "\\", "é", "\x1b", "\t"])


dumps = []
for _ in range(1500):
    cols = {}
    for _ in range(rng.randint(0, 5)):
        cols[rname()] = rvalue()
    text = yaml.safe_dump({"columns": dict(sorted(cols.items()))}, sort_keys=False, allow_unicode=True,
                          default_flow_style=False)
    if max(len(line) for line in text.splitlines()) >= 78:
        continue  # PyYAML folds long lines; the Go port doesn't
    dumps.append({"cols": cols, "text": text})

TEMPL = ["columns:\n  {k}: {v}\n", "columns: {{{k}: {v}}}\n", "columns:\n  ? {k}\n  : {v}\n", "{k}: {v}\n",
         "columns:\n  {k}: {v}\n  {k}: 3\n", "base: &b\n  {k}: {v}\ncolumns:\n  <<: *b\n  z: 2\n",
         "a: &a\n  {k}: {v}\nb: &b\n  {k}: 1\n  w: 2\ncolumns:\n  <<: [*a, *b]\n  z: 2\n",
         "columns:\n  <<: {v}\n", "columns:\n  <<: [{v}]\n",
         "columns: &c\n  {k}: {v}\nother: *c\n", "columns:\n  {k}: !!str {v}\n",
         "columns:\n  {k}: !!python/object:os.system {v}\n", "columns:\n  !!python/name:os.system {k}: 3\n",
         "columns: {v}\n", "columns:\n- {k}\n", "---\ncolumns:\n  {k}: {v}\n---\nx: 1\n", "\ufeffcolumns:\n  {k}: {v}\n",
         "columns:\n\t{k}: {v}\n", "%YAML 1.1\n---\ncolumns:\n  {k}: {v}\n", "%YAML 1.2\n---\ncolumns:\n  {k}: {v}\n",
         "%YAML 2.0\n---\ncolumns:\n  {k}: {v}\n", "%TAG !e! tag:example.com,2000:\n---\ncolumns:\n  {k}: {v}\n",
         "columns:\n  {k}: |\n    {v}\n", "columns:\n  {k}: >-\n    {v}\n", "columns:\n  [a, b]: 3\n",
         "columns:\n  {{a: 1}}: 3\n", "columns:\n  {k}: [1]\n", "columns: !!map\n  {k}: {v}\n",
         "columns: !!omap\n  - {k}: {v}\n", "columns: !!set\n  ? {k}\n", "x: !!set\n  ? {k}\ncolumns:\n  {k}: {v}\n",
         "x: !!omap\n  - a: 1\n  - b\ncolumns:\n  {k}: {v}\n", "x: !!pairs [[a, b]]\ncolumns:\n  {k}: {v}\n",
         "columns:\n  {k}: {v} # c\n", "columns:\n  {k}: '{v}'\n", "columns:\n  {k}: \"{v}\"\n", "columns:\n  '{k}': {v}\n",
         "columns:\n  \"{k}\": {v}\n", "! columns:\n  {k}: {v}\n", "columns:\n  {k}: !!int {v}\n",
         "columns:\n  {k}: !!float {v}\n", "columns:\n  {k}: !!bool {v}\n", "columns:\n  {k}: !!null {v}\n",
         "columns:\n  {k}: !!timestamp {v}\n", "columns:\n  !!binary aGk=: {v}\n", "columns:\n  {k}: !custom 3\n",
         "columns:\n  {k}: !<tag:yaml.org,2002:str> {v}\n", "columns:\n  ? |\n    {k}\n  : {v}\n",
         "columns:\n  {k}:\n", "columns:\n  {k}: &a {v}\n  y: *a\n", "a: &a [*a]\ncolumns:\n  {k}: {v}\n",
         "columns:\n  {k}: 3\n...\n", "columns:\n  {k}: 0b_\n", "columns:\n  {k}: 0x_\n", "x: =\ncolumns:\n  {k}: {v}\n",
         "x: [=]\n", "=: 3\ncolumns:\n  =: 4\n", "columns:\n  'a\n\n   b': .2f\n  \"c\n   d\": 2\n",
         "columns:\n  1.0: 3\n  .inf: 4\n  1_000.5: 5\n  0x10: 6\n  ~: 7\n  yes: 8\n  2026-01-02 03:04:05: 9\n"
         "  2026-01-02: 1\n  1:30: 2\n  0b11: 3\n  017: 4\n  1e3: 5\n  2026-1-2 3:4:05.5 +5: 6\n  2026-01-02T03:04:05Z: 7\n"
         "  !!int 0o7: 8\n  !!float 1: 9\n  1.5e+3: 10\n  -.inf: 11\n  2026-01-02 03:04:05.1234567 -05:30: 12\n",
         "columns:\n  1: 3\n  true: 4\n  1.0: 5\n", "columns:\n  a: 08\n  b: 07\n  c: 0o7\n  d: 09.5\n",
         "columns:\n  2026-13-01: 3\n", "columns:\n  !!int x: 3\n", "columns:\n  !!timestamp x: 3\n",
         "columns:\n  !!bool maybe: 3\n", "columns:\n  !!float x: 3\n", "columns:\n  a: !!str\n"]
VALS = ["3", "17", "18", "-1", "017", "0x3", "0b11", "1_0", "1:05", "yes", "~", "null", ".5", "'.5'", ".2f", "'.2f'",
        "%Y", "'%Y'", "\"%Y\"", "1e3", ".inf", "3.0", "0", "00", "08", "0o7", "+3", "''", "\"\"", "\".2\\x1b\"",
        "\"\\e<5\"", "[1]", "{a: 1}", "!!str 3", "2026-01-02", "=", "<<", "99999999999999999999", "0x_", ",d",
        "\">12\"", "x", "'x'", "'%-d %_m'", "abc", "maybe", "Y"]
KEYS = ["ra", "yes", "1", "~", "null", "''", "\"\"", "'a: b'", "\"\\e]0;x\\a\"", "0x10", "1.5", "2026-01-02", "<<",
        "=", "a b", "é", "-x", "? x", "'? x'", "\"\\t\"", "on", "NULL", "Off", "1:30", ".NaN"]


def load(text: bytes):
    with tempfile.TemporaryDirectory() as d:
        p = Path(d) / "formats.yaml"
        p.write_bytes(text)
        try:
            return {"out": C.load_formats(p)}
        except Exception as e:  # noqa: BLE001 (Python crashes on some: the Go port refuses them)
            return {"error": f"{type(e).__name__}: {e}"[:200]}


loads = []
seen = set()
for _ in range(4000):
    t = rng.choice(TEMPL)
    text = t.format(k=rng.choice(KEYS), v=rng.choice(VALS))
    if rng.random() < 0.05:
        text = text[:rng.randint(0, len(text))]
    if text in seen:
        continue
    seen.add(text)
    loads.append({"text": text, **load(text.encode())})
for name, b in {"latin1": "columns:\n  r\xe9: 3\n".encode("latin-1"), "bom": b"\xef\xbb\xbfcolumns:\n  ra: 3\n",
                "utf16": "columns:\n  ra: 3\n".encode("utf-16"), "nul": b"columns:\n  ra: 3\x00\n", "empty": b"",
                "comment": b"# hi\n", "scalar": b"3\n", "zero": b"0\n", "list": b"- 1\n", "esc": b"columns:\n  ra\x1b: 3\n",
                "dupcols": b"columns:\n  ra: 3\ncolumns:\n  dec: 4\n", "tab": b"columns:\n  ra:\t3\n",
                "crlf": b"columns:\r\n  ra: 3\r\n  dec: .2f\r\n", "cr": b"columns:\r  ra: 3\r",
                "nel": "columns:\n  ra: 3\x85  dec: 4\n".encode(), "ls": "columns:\n  ra: 3\u2028  dec: 4\n".encode(),
                "nelq": "columns:\n  ra: \"a\x85b\"\n".encode(), "c1": "columns:\n  r\x9b: 3\n".encode(),
                "bomin": "columns:\n  r\ufeff: 3\n".encode(), "del": b"columns:\n  r\x7f: 3\n"}.items():
    loads.append({"name": name, "hex": b.hex(), **load(b)})

with open(os.path.join(HERE, "yaml_cases.json"), "w", encoding="utf-8") as f:
    json.dump({"dump": dumps, "load": loads}, f, ensure_ascii=False, indent=0)
print(len(dumps), "dumps,", len(loads), "loads")
