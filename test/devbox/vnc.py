#!/usr/bin/env python3
"""Presses keys and clicks on the devbox screen through its VNC server
(127.0.0.1:5900, no password), as a viewer would.

usage: vnc.py STEP...
  a key or combination:  F1  Super+Tab  Shift+F10  Return  t
  type:TEXT              type text, one key per character
  click:X,Y  rclick:X,Y  left or right click
  sleep:SECONDS
"""
import socket
import struct
import sys
import time

KEYSYMS = {
    "Super": 0xFFEB, "Shift": 0xFFE1, "Ctrl": 0xFFE3, "Alt": 0xFFE9, "Tab": 0xFF09,
    "Return": 0xFF0D, "Escape": 0xFF1B, "Delete": 0xFFFF, "BackSpace": 0xFF08,
    "Up": 0xFF52, "Down": 0xFF54, "Left": 0xFF51, "Right": 0xFF53, "Menu": 0xFF67,
    **{f"F{n}": 0xFFBE + n - 1 for n in range(1, 13)},
}


def keysym(name):
    if name in KEYSYMS:
        return KEYSYMS[name]
    if len(name) == 1:
        return ord(name)
    raise SystemExit(f"vnc.py: unknown key {name!r}")


def connect(host="127.0.0.1", port=5900):
    s = socket.create_connection((host, port), timeout=5)
    s.recv(12)  # "RFB 003.008\n"
    s.sendall(b"RFB 003.008\n")
    types = s.recv(s.recv(1)[0])
    if 1 not in types:
        raise SystemExit(f"vnc.py: the server wants authentication ({list(types)})")
    s.sendall(b"\x01")  # security type None
    if struct.unpack(">I", s.recv(4))[0] != 0:
        raise SystemExit("vnc.py: security handshake failed")
    s.sendall(b"\x01")  # shared session
    init = s.recv(24)
    s.recv(struct.unpack(">I", init[20:24])[0])  # the server's name
    return s


def key(s, sym, down):
    s.sendall(struct.pack(">BBxxI", 4, 1 if down else 0, sym))


def press(s, combo):
    syms = [keysym(k) for k in combo.split("+")] if len(combo) > 1 else [keysym(combo)]
    for k in syms:
        key(s, k, True)
        time.sleep(0.03)
    for k in reversed(syms):
        key(s, k, False)
        time.sleep(0.03)


def click(s, x, y, button=1):
    for mask in (0, 1 << (button - 1), 0):
        s.sendall(struct.pack(">BBHH", 5, mask, x, y))
        time.sleep(0.05)


def main(steps):
    s = connect()
    for step in steps:
        kind, _, arg = step.partition(":")
        if kind in ("click", "rclick") and arg:
            x, y = arg.split(",")
            click(s, int(x), int(y), 3 if kind == "rclick" else 1)
        elif kind == "sleep" and arg:
            time.sleep(float(arg))
            continue
        elif kind == "type" and arg:
            for ch in arg:
                press(s, ch)
        else:
            press(s, step)
        time.sleep(0.4)
    s.close()


if __name__ == "__main__":
    main(sys.argv[1:])
