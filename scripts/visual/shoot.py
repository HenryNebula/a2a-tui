#!/usr/bin/env python3
"""Visual eval harness for a2a-tui.

Drives the TUI inside a hermetic tmux server (fixed geometry, forced
256-color), scripts key sequences against the fixture agent, and captures
each named state three ways: plain text, ANSI, and a rendered PNG (ANSI →
HTML → headless Chrome screenshot).

Usage: shoot.py [--round r1] [--only name,name,...]
Output: scripts/visual/out/<round>/{*.txt,*.ansi,*.html,*.png}
"""
import argparse
import os
import re
import shutil
import signal
import subprocess
import sys
import time
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
TUI = os.path.join(REPO, "bin", "a2a-tui")
FIXTURE = os.path.join(REPO, "bin", "a2a-fixture-agent")
HERE = os.path.dirname(os.path.abspath(__file__))
CONVERTER = os.path.join(HERE, "ansi2html.py")
CHROME = "/usr/bin/google-chrome"

# Rendering metrics — must match ansi2html.py defaults (15px DejaVu Sans Mono
# has a 9.034px advance; line-height 18px; 10px padding).
FS, LH, PAD, ADVANCE = 15, 18, 10, 9.034

PORT = 8879
SOCK = None  # per-run tmux socket, e.g. a2avis-<pid>
PROC = None  # fixture agent subprocess
OUT = None


def run(cmd, **kw):
    return subprocess.run(cmd, capture_output=True, text=True, **kw)


def tmux(*args, check=True):
    r = run(["tmux", "-L", SOCK, *args])
    if check and r.returncode != 0:
        raise RuntimeError(f"tmux {' '.join(args)}: {r.stderr.strip()}")
    return r


def start_fixture():
    global PROC
    PROC = subprocess.Popen([FIXTURE, "-addr", f"127.0.0.1:{PORT}"],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = f"http://127.0.0.1:{PORT}/.well-known/agent-card.json"
    for _ in range(50):
        try:
            urllib.request.urlopen(url, timeout=1)
            return f"http://127.0.0.1:{PORT}"
        except Exception:
            time.sleep(0.1)
    raise RuntimeError("fixture agent did not come up")


def start_session(cols, rows, agent=None, name="vis"):
    """Create a hermetic tmux session running the TUI."""
    conf = os.path.join(OUT, "tmux.conf")
    with open(conf, "w") as f:
        f.write("set -g default-terminal tmux-256color\n")
        f.write("set -g exit-empty off\n")
    cmd = [TUI]
    if agent:
        cmd += ["--agent", agent]
    tmux("-f", conf, "new-session", "-d", "-x", str(cols), "-y", str(rows),
         "-s", name, f"exec {' '.join(cmd)}")
    return name


def kill_session(name="vis"):
    tmux("kill-session", "-t", name, check=False)


def send(keys):
    tmux("send-keys", "-t", "vis", "-l", keys)


def key(k):
    tmux("send-keys", "-t", "vis", k)


def type_msg(text):
    send(text)
    key("Enter")


def pane_text():
    return tmux("capture-pane", "-p", "-t", "vis").stdout


def wait_for(pattern, timeout=8, what=""):
    rx = re.compile(pattern, re.IGNORECASE)
    deadline = time.time() + timeout
    while time.time() < deadline:
        if rx.search(pane_text()):
            return True
        time.sleep(0.15)
    raise TimeoutError(f"waiting for {pattern!r} {what}\n--- pane ---\n{pane_text()}")


def shot(name, cols, rows):
    base = os.path.join(OUT, name)
    ansi = tmux("capture-pane", "-e", "-p", "-t", "vis").stdout
    with open(base + ".txt", "w") as f:
        f.write(pane_text())
    with open(base + ".ansi", "w") as f:
        f.write(ansi)
    html_path = base + ".html"
    r = run([sys.executable, CONVERTER, base + ".ansi", html_path,
             "--cols", str(cols), "--rows", str(rows)])
    if r.returncode != 0:
        raise RuntimeError(f"ansi2html failed: {r.stderr}")
    w = round(cols * ADVANCE) + 2 * PAD + 4
    h = rows * LH + 2 * PAD + 4
    r = run([CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars",
             "--no-first-run", "--force-device-scale-factor=2",
             f"--window-size={w},{h}",
             f"--user-data-dir={os.path.join(OUT, 'chrome')}",
             f"--screenshot={base}.png", "file://" + html_path], timeout=60)
    if r.returncode != 0:
        raise RuntimeError(f"chrome failed: {r.stderr[:500]}")
    print(f"  shot {name}")


def run_main(agent_url, c, r):
    """Full walkthrough on the default geometry."""
    start_session(c, r)
    wait_for(r"connect with /connect", what="fresh screen")
    time.sleep(0.4)
    shot("01-fresh-disconnected", c, r)

    key("F1")
    wait_for(r"· help", what="help overlay")
    time.sleep(0.8)
    shot("02-help-overlay", c, r)
    key("Escape")
    time.sleep(0.8)

    type_msg(f"/connect {agent_url}")
    wait_for(r"connected to", what="connect")
    time.sleep(0.4)
    shot("03-connected", c, r)

    type_msg("hello there, nice to meet you")
    wait_for(r"echo: hello there")
    time.sleep(0.8)
    shot("04-echo-chat", c, r)

    type_msg("slow 4")
    wait_for(r"working · #", what="working state")
    time.sleep(0.8)
    shot("05-working-spinner", c, r)
    wait_for(r"● completed · #\S+ · took", timeout=12, what="slow task done")
    time.sleep(0.8)

    type_msg("inputreq")
    wait_for(r"input-required", what="input-required state")
    time.sleep(0.8)
    shot("06-input-required", c, r)
    type_msg("blue")
    wait_for(r"you answered", timeout=8)
    time.sleep(0.8)
    shot("07-input-required-answered", c, r)

    type_msg("a2ui form")
    time.sleep(1.2)
    key("C-f")
    time.sleep(0.8)
    shot("08-surface-form", c, r)
    send("Ada Lovelace")
    time.sleep(0.4)
    key("Tab")
    time.sleep(0.4)
    shot("09-surface-form-filled", c, r)
    key("Escape")
    time.sleep(0.8)

    type_msg("artifact")
    time.sleep(1.5)
    shot("10-artifact-markdown", c, r)

    key("C-k")
    time.sleep(0.8)
    shot("11-tasks-dashboard", c, r)
    key("Enter")
    time.sleep(0.8)
    shot("12-task-detail", c, r)
    key("Escape")
    time.sleep(0.2)
    key("Escape")
    time.sleep(0.8)

    type_msg("/wire on")
    type_msg("ping")
    wait_for(r"frames: ", what="wire frames")
    time.sleep(0.8)
    shot("13-wire-pane", c, r)
    key("C-t")
    time.sleep(0.8)

    key("C-e")
    time.sleep(0.8)
    shot("14-console", c, r)
    key("Tab")
    time.sleep(0.8)
    shot("15-console-next-preset", c, r)
    key("Escape")
    time.sleep(0.2)

    key("C-g")
    time.sleep(0.8)
    shot("16-agent-card", c, r)
    key("C-t")
    time.sleep(0.8)

    type_msg("fail")
    wait_for(r"● failed", timeout=8)
    time.sleep(0.8)
    shot("17-error-shown", c, r)

    kill_session()


def run_narrow(agent_url):
    c, r = 80, 24
    start_session(c, r, agent=agent_url)
    wait_for(r"connected to", what="narrow connect")
    type_msg("hello in a narrow terminal")
    wait_for(r"echo: hello")
    time.sleep(0.8)
    shot("20-narrow-echo", c, r)
    key("F1")
    time.sleep(0.4)
    shot("21-narrow-help", c, r)
    key("Escape")
    type_msg("a2ui form")
    time.sleep(1.2)
    key("C-f")
    time.sleep(0.8)
    shot("22-narrow-surface", c, r)
    key("Escape")
    kill_session()


def run_tiny(agent_url):
    c, r = 62, 18
    start_session(c, r, agent=agent_url)
    wait_for(r"connected to", what="tiny connect")
    time.sleep(0.8)
    shot("23-tiny-connected", c, r)
    type_msg("hi")
    wait_for(r"echo: hi")
    time.sleep(0.2)
    shot("24-tiny-chat", c, r)
    key("F1")
    time.sleep(0.4)
    shot("25-tiny-help", c, r)
    key("Escape")
    kill_session()


def main():
    global SOCK, OUT
    ap = argparse.ArgumentParser()
    ap.add_argument("--round", default="r1")
    ap.add_argument("--only", default="")
    args = ap.parse_args()

    OUT = os.path.join(HERE, "out", args.round)
    shutil.rmtree(OUT, ignore_errors=True)
    os.makedirs(OUT, exist_ok=True)
    SOCK = f"a2avis-{os.getpid()}"

    agent_url = start_fixture()
    print(f"fixture agent: {agent_url}")
    try:
        runs = {"main": lambda: run_main(agent_url, 120, 34),
                "narrow": lambda: run_narrow(agent_url),
                "tiny": lambda: run_tiny(agent_url)}
        only = [s.strip() for s in args.only.split(",") if s.strip()]
        for name, fn in runs.items():
            if only and name not in only:
                continue
            print(f"[{name}]")
            fn()
    finally:
        tmux("kill-server", check=False)
        if PROC:
            PROC.send_signal(signal.SIGTERM)
            PROC.wait(timeout=5)
    print("done")


if __name__ == "__main__":
    main()
