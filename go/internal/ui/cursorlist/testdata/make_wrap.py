"""Writes wrap.json: Textual's wrapping (Content._wrap_and_format, which uses
Rich's divide_line) of random texts at widths 1-12, the reference for
cursorlist.Wrap. Run with Python pqx's venv (Textual 8.2):

    /path/to/venv/bin/python make_wrap.py
"""
import json
import random

from textual.content import Content

rng = random.Random(7)
alphabet = list("abcdefgh_") + [" "] * 4 + ["日", "本", "語", "😀", "é", "é", "‍", "️", "\t"]
cases = []
for n in range(3000):
    k = rng.randint(0, 24)
    text = "".join(rng.choice(alphabet) for _ in range(k)).replace("\t", " ")
    if n % 10 == 0:
        text = " " + text  # leading spaces
    width = rng.randint(1, 12)
    try:
        lines = [fl.content.plain for fl in Content(text)._wrap_and_format(width)]
    except IndexError:
        continue  # (Rich's _split_text fails on some joiner sequences: no reference)
    cases.append({"text": text, "width": width, "lines": lines})
for text, width in [("midpointMjdTai_flag_degraded  bool", 32), ("y" * 63 + "  i64", 32), ("z" * 64 + "  f64", 32),
                    (" gca日fh_", 8), ("ab日本", 1)]:
    cases.append({"text": text, "width": width, "lines": [fl.content.plain for fl in Content(text)._wrap_and_format(width)]})
json.dump(cases, open("wrap.json", "w"), ensure_ascii=False, indent=0)
