#!/usr/bin/env python3
"""Send HL7 v2 messages over MLLP and print each ACK.

    tools/mllp_send.py localhost 6661 samples/adt/a01_admit.hl7

Files hold one or more messages separated by blank lines; segments may end in
LF, CRLF or CR. Standard library only.
"""
import socket
import sys


def messages(path):
    with open(path, newline="") as f:
        text = f.read().replace("\r\n", "\n").replace("\r", "\n")
    for block in text.split("\n\n"):
        segments = [line for line in block.split("\n") if line.strip()]
        if segments:
            yield "\r".join(segments) + "\r"


def send(host, port, msg):
    with socket.create_connection((host, port), timeout=30) as s:
        s.sendall(b"\x0b" + msg.encode() + b"\x1c\r")
        ack = b""
        while not ack.endswith(b"\x1c\r"):
            chunk = s.recv(65536)
            if not chunk:
                break
            ack += chunk
    return ack.strip(b"\x0b\x1c\r\n").decode().split("\r")


def main():
    if len(sys.argv) < 4:
        sys.exit(__doc__)
    host, port = sys.argv[1], int(sys.argv[2])
    for path in sys.argv[3:]:
        for msg in messages(path):
            msh = msg.split("\r", 1)[0].split("|")
            msa = next((seg for seg in send(host, port, msg) if seg.startswith("MSA")), "no MSA in ACK")
            print(f"{msh[8]} {msh[9]}: {msa}")


if __name__ == "__main__":
    main()
