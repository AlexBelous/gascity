#!/usr/bin/python3 -I
"""Model-side fixed action client: bounded entry wait, exactly one effect request."""
import json
import socket
import sys
import time
SOCKET = "/run/gc-repairer-limited208/action.sock"

def request(op):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(12)
        client.connect(SOCKET)
        client.sendall(json.dumps({"op":op},separators=(",",":")).encode())
        client.shutdown(socket.SHUT_WR)
        raw = client.recv(257)
        if len(raw) > 256:
            raise ValueError("frame")
        value = json.loads(raw)
        if type(value) is not dict:
            raise ValueError("object")
        return value

def baseline(value):
    return (type(value) is dict and set(value) == {"entered", "unit", "state", "main_pid", "invocation_id", "result"}
            and value["entered"] is True and value["unit"] == "gc-repairer-action-208-fixture.service"
            and type(value["main_pid"]) is int and value["main_pid"] >= 0
            and value["state"] in ("active", "inactive", "failed", "activating", "deactivating")
            and type(value["invocation_id"]) is str)

def main():
    if len(sys.argv) != 2 or sys.argv[1] not in ("status", "restart"):
        return 1
    mode = sys.argv[1]
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            value = request("status")
            if baseline(value):
                if mode == "status":
                    print(json.dumps(value,sort_keys=True,separators=(",",":")))
                    return 0
                if value["state"] != "inactive" or value["main_pid"] != 0:
                    return 1
                # Never retry the effect, even after a lost response.
                return 0 if request("restart") == {"restarted":True} else 1
        except (OSError, ValueError):
            return 1
        time.sleep(.15)
    return 1

if __name__ == "__main__":
    sys.exit(main())
