#!/usr/bin/env python3
"""Convert tmux `capture-pane -e -p` output to a self-contained HTML page.

Handles the SGR subset xterm-256color terminals emit (attributes, 16/256/true
color fg+bg, reverse video). Non-SGR CSI/ESC sequences are stripped — the
screen content is already laid out in the capture, only styling matters.

Usage: ansi2html.py <input.ansi> <output.html> [--cols N] [--rows N]
"""
import argparse
import html
import re
import sys

CSI = re.compile(r"(\x1b\[[0-9;?]*[A-Za-z])")
OSC = re.compile(r"\x1b\].*?(\x07|\x1b\\)")

# Standard xterm palette: 16 base colors, 6x6x6 cube, 24-step grayscale.
_CUBE = (0, 95, 135, 175, 215, 255)


def _base16():
    # Classic xterm base palette.
    names = [
        "#000000", "#cd0000", "#00cd00", "#cdcd00",
        "#0000ee", "#cd00cd", "#00cdcd", "#e5e5e5",
        "#7f7f7f", "#ff0000", "#00ff00", "#ffff00",
        "#5c5cff", "#ff00ff", "#00ffff", "#ffffff",
    ]
    return names


PALETTE = _base16()
for r in _CUBE:
    for g in _CUBE:
        for b in _CUBE:
            PALETTE.append(f"#{r:02x}{g:02x}{b:02x}")
for i in range(24):
    v = 8 + i * 10
    PALETTE.append(f"#{v:02x}{v:02x}{v:02x}")


def color_from_params(params, base):
    """params is the SGR param list after the 38/48 introducer index."""
    if len(params) < 2:
        return None
    mode = params[1]
    if mode == "5" and len(params) >= 3:
        try:
            n = int(params[2])
        except ValueError:
            return None
        if 0 <= n < 256:
            return PALETTE[n]
        return None
    if mode == "2" and len(params) >= 5:
        try:
            r, g, b = (int(x) for x in params[2:5])
        except ValueError:
            return None
        return f"#{r:02x}{g:02x}{b:02x}"
    return None


class State:
    def __init__(self):
        self.fg = None
        self.bg = None
        self.bold = False
        self.dim = False
        self.italic = False
        self.underline = False
        self.reverse = False
        self.strike = False

    def style(self):
        # Resolve defaults BEFORE reversing: reverse-video on default
        # colors must still produce visible contrast (cursor cells).
        fg = self.fg or "#e6e6e6"
        bg = self.bg or "#12151a"
        if self.reverse:
            fg, bg = bg, fg
        css = []
        if fg:
            css.append(f"color:{fg}")
        if bg:
            css.append(f"background:{bg}")
        if self.bold:
            css.append("font-weight:bold")
        if self.dim:
            css.append("opacity:.55")
        if self.italic:
            css.append("font-style:italic")
        if self.underline:
            css.append("text-decoration:underline")
        if self.strike:
            css.append("text-decoration:line-through")
        return ";".join(css)


def apply_sgr(state, m):
    """Apply one SGR sequence (already stripped of ESC [ m)."""
    body = m[2:-1]
    params = [p for p in body.split(";")] or [""]
    i = 0
    while i < len(params):
        p = params[i] or "0"
        try:
            n = int(p)
        except ValueError:
            i += 1
            continue
        if n == 0:
            state.__init__()
        elif n == 1:
            state.bold = True
        elif n == 2:
            state.dim = True
        elif n == 3:
            state.italic = True
        elif n == 4:
            state.underline = True
        elif n == 7:
            state.reverse = True
        elif n == 9:
            state.strike = True
        elif n == 22:
            state.bold = state.dim = False
        elif n == 23:
            state.italic = False
        elif n == 24:
            state.underline = False
        elif n == 27:
            state.reverse = False
        elif n == 29:
            state.strike = False
        elif 30 <= n <= 37:
            state.fg = PALETTE[n - 30]
        elif n == 39:
            state.fg = None
        elif 40 <= n <= 47:
            state.bg = PALETTE[n - 40]
        elif n == 49:
            state.bg = None
        elif 90 <= n <= 97:
            state.fg = PALETTE[n - 90 + 8]
        elif 100 <= n <= 107:
            state.bg = PALETTE[n - 100 + 8]
        elif n in (38, 48):
            # Extended color: 38;5;n or 38;2;r;g;b — consume the rest.
            c = color_from_params(params[i:], n)
            if n == 38:
                state.fg = c
            else:
                state.bg = c
            break
        i += 1


PAGE = """<!DOCTYPE html>
<html><head><meta charset="utf-8"><style>
html, body {{ margin:0; padding:0; background:{bg}; overflow:hidden; }}
pre {{ margin:0; padding:{pad}px; font-family:'DejaVu Sans Mono',monospace;
       font-size:{fs}px; line-height:{lh}px; white-space:pre; color:{fg};
       background:{bg}; }}
</style></head><body><pre>{body}</pre></body></html>"""


def convert(text, cols=120, rows=34, fs=15, lh=18, pad=10,
            bg="#12151a", fg="#e6e6e6"):
    text = OSC.sub("", text)
    out = []
    state = State()
    cur_style = state.style()
    for tok in CSI.split(text):
        if tok.startswith("\x1b[") and tok.endswith("m"):
            apply_sgr(state, tok)
            new_style = state.style()
            if new_style != cur_style:
                out.append("</span>")
                out.append(f'<span style="{html.escape(new_style)}">')
                cur_style = new_style
            continue
        if tok.startswith("\x1b"):
            continue  # other escapes: drop
        out.append(html.escape(tok).replace("\x00", ""))
    body = "".join(out)
    if cur_style:
        body = f'<span style="{html.escape(cur_style)}">{body}</span>'
    return PAGE.format(bg=bg, fg=fg, fs=fs, lh=lh, pad=pad, body=body)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("input")
    ap.add_argument("output")
    ap.add_argument("--cols", type=int, default=120)
    ap.add_argument("--rows", type=int, default=34)
    args = ap.parse_args()
    with open(args.input, "rb") as f:
        text = f.read().decode("utf-8", "replace")
    page = convert(text, args.cols, args.rows)
    with open(args.output, "w") as f:
        f.write(page)
    sys.stderr.write(f"wrote {args.output}\n")


if __name__ == "__main__":
    main()
