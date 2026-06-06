#!/usr/bin/env python3
"""Minimal mock block builder accepting eth_sendBundle JSON-RPC."""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    bundles = 0

    def log_message(self, fmt, *args):
        pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        Handler.bundles += 1
        resp = {"jsonrpc": "2.0", "id": 1, "result": {"bundleHash": "0xmock"}}
        data = json.dumps(resp).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18545
    srv = HTTPServer(("127.0.0.1", port), Handler)
    print(f"mock builder on :{port}", flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
