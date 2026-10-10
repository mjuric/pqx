"""Write rich_colours.json: how Python pqx (Textual on Rich) reduces colours.

Run with the reference venv (Rich, Textual and pqx importable):

    /root/parquet-explorer/.venv/bin/python gen_rich_colours.py > rich_colours.json

- "detect": the colour system Textual's console picks for an environment (each case
  run in a fresh process, as Textual reads TEXTUAL_COLOR_SYSTEM on import), as Textual
  renders with it (no system: truecolor).
- "colours": colours pqx draws (every 256-colour index, the colourmaps' colours and
  their 256-colour indices, the named themes' palettes, Textual's ANSI palette) with
  the SGR parameters Python pqx sends for them on a truecolor, a 256- and a 16-colour
  terminal: Textual takes each colour as its own Color (a 256-colour index beyond the
  16 ANSI colours becomes its xterm truecolor value), and Rich reduces that.
"""
import json
import os
import subprocess
import sys

from rich.color import Color, ColorSystem
from textual.color import Color as TextualColor

from pqx import plots

# environments: TERM, COLORTERM, TEXTUAL_COLOR_SYSTEM (None: unset)
ENVS = [
    {"TERM": "screen"}, {"TERM": "screen-256color"}, {"TERM": "screen.xterm-256color"},
    {"TERM": "tmux-256color"}, {"TERM": "xterm"}, {"TERM": "xterm-256color"},
    {"TERM": "xterm-16color"}, {"TERM": "xterm-color"}, {"TERM": "xterm-direct"},
    {"TERM": "xterm-kitty"}, {"TERM": "kitty"}, {"TERM": "256color"}, {"TERM": "linux"},
    {"TERM": "vt100"}, {"TERM": "dumb"}, {"TERM": "unknown"}, {"TERM": "DUMB"}, {"TERM": " dumb "},
    {"TERM": " Xterm-256Color "}, {"TERM": ""}, {},
    {"TERM": "screen", "COLORTERM": "truecolor"}, {"TERM": "screen", "COLORTERM": "24bit"},
    {"TERM": "xterm", "COLORTERM": "TrueColor"}, {"TERM": "xterm", "COLORTERM": "yes"},
    {"TERM": "xterm-256color", "COLORTERM": "truecolor"}, {"TERM": "linux", "COLORTERM": "truecolor"},
    {"TERM": "dumb", "COLORTERM": "truecolor"},
    {"TERM": "xterm-256color", "TEXTUAL_COLOR_SYSTEM": "standard"},
    {"TERM": "screen", "TEXTUAL_COLOR_SYSTEM": "256"},
    {"TERM": "screen", "TEXTUAL_COLOR_SYSTEM": "truecolor"},
    {"TERM": "screen", "TEXTUAL_COLOR_SYSTEM": "auto"},
    {"TERM": "xterm-256color", "COLORTERM": "truecolor", "TEXTUAL_COLOR_SYSTEM": "standard"},
]

PROBE = (
    "from textual.app import App\n"
    "from rich.console import ColorSystem\n"
    "s = App().console._color_system or ColorSystem.TRUECOLOR\n"
    "print({ColorSystem.STANDARD: 'standard', ColorSystem.EIGHT_BIT: '256',"
    " ColorSystem.TRUECOLOR: 'truecolor'}[s])\n"
)


def detect(env):
    e = {k: v for k, v in os.environ.items()
         if k not in ("TERM", "COLORTERM", "TEXTUAL_COLOR_SYSTEM", "NO_COLOR")}
    e.update(env)
    out = subprocess.run([sys.executable, "-c", PROBE], env=e, capture_output=True, text=True, check=True)
    return out.stdout.strip()


def codes(spec, system):
    colour = TextualColor.from_rich_color(Color.parse(spec)).rich_color
    return ";".join(colour.downgrade(system).get_ansi_codes())


def colour_specs():
    specs = [f"color({i})" for i in range(256)]
    for cmap in plots.COLORMAPS:
        for dark in (True, False):
            for k in range(101):
                hexc = plots.cmap_color(cmap, k / 100, dark)
                specs += [hexc, f"color({plots.xterm256(hexc)})"]
    from textual.theme import BUILTIN_THEMES
    for name in ("tokyo-night", "dracula", "catppuccin-mocha", "nord", "gruvbox"):
        th = BUILTIN_THEMES[name]
        for v in (th.primary, th.secondary, th.accent, th.foreground, th.background, th.surface,
                  th.panel, th.warning, th.error, th.success):
            if v:
                specs.append(v.lower())
    from textual._ansi_theme import MONOKAI
    specs += ["#%02x%02x%02x" % tuple(c) for c in MONOKAI.ansi_colors]
    seen, out = set(), []
    for s in specs:
        if s not in seen:
            seen.add(s)
            out.append(s)
    return out


def main():
    detect_cases = [{"env": env, "system": detect(env)} for env in ENVS]
    colours = [{"spec": s, "truecolor": codes(s, ColorSystem.TRUECOLOR), "256": codes(s, ColorSystem.EIGHT_BIT),
                "16": codes(s, ColorSystem.STANDARD)}
               for s in colour_specs()]
    json.dump({"detect": detect_cases, "colours": colours}, sys.stdout, indent=0)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
