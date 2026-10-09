"""Write entries.json: Details entries laid out by Python pqx's EntryGrid (Rich), the spec
for the Go pane's layout (layout_test.go). Run from the repository root:

    PYTHONPATH=$PWD python go/internal/ui/detail/testdata/make_entries.py
"""
import json
import os
import random

from rich.console import Console
from rich.segment import Segment
from rich.text import Text

from pqx.widgets import EntryGrid, _LazyEntry

rnd = random.Random(5)
chars = "abcxyz0123456789.-e+ 日本é\t"


def rand(n):
    return "".join(rnd.choice(chars) for _ in range(n))


cases = []
console = Console(width=200, color_system=None, legacy_windows=False)
while len(cases) < 400:
    width, nw = rnd.randint(4, 70), rnd.randint(1, 23)
    if width - nw - 2 < 1:
        continue
    name = rand(rnd.randint(1, 30)).replace(" ", "_").replace("\t", "_")
    value = Text(rand(rnd.choice([0, 1, 5, 10, 24, 25, 30, 80, 300])))
    unit = ""
    if rnd.random() < .5:
        unit = rand(3)
        value.append("  " + unit, "dim")
    derived = ""
    if rnd.random() < .3:
        derived = rand(rnd.randint(1, 60))
        value.append("\n· " + derived, "dim")
    grid = EntryGrid(Text(name, style="bold"), value, nw)
    segs = console.render(grid, console.options.update_width(width).update(highlight=False))
    lines = ["".join(s.text for s in ln).rstrip("\n") for ln in Segment.split_and_crop_lines(segs, width, pad=True)]
    cases.append({"width": width, "nw": nw, "name": name, "value": value.plain, "lines": lines,
                  "one_line": _LazyEntry.one_line(name, value, nw, width)})

here = os.path.dirname(os.path.abspath(__file__))
with open(os.path.join(here, "entries.json"), "w") as f:
    json.dump(cases, f, ensure_ascii=False, indent=0)
    f.write("\n")
