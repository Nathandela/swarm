#!/usr/bin/env python3
"""A TLS terminator for the handset demonstration (PB-OPS-1). NOT a production front end.

WHY THIS EXISTS. swarm-relay serves plain ws:// and only plain ws://: Server.Start binds a
net.Listener and sets s.url = "ws://"+addr (internal/remote/relay/server.go), Config.TLSMode is
read by nothing that serves, and the comment on Server.URL says the cleartext is intentional
because E2EE does not depend on TLS. PB-NET-2 nevertheless refuses cleartext to anything but a
loopback literal, so a phone on the LAN cannot reach the relay at all without something in
front of it holding a certificate. This is that something, in the standard library, so the
PB-OPS-1 runbook is a sequence an operator can actually run rather than a sequence that assumes
a reverse proxy nobody has installed.

WHAT IT IS NOT. It terminates TLS and pipes bytes; it has no access control, no rate limiting,
no logging discipline, no restart supervision and no certificate reloading. Production TLS
termination, VPS provisioning and image publishing are Phase C by the section 6.18 scope
correction. Do not deploy this.

    relay-tls-terminator.py --listen 0.0.0.0:8443 --target 127.0.0.1:9443 \
        --cert relay.crt --key relay.key

Terminating at the TCP layer rather than proxying HTTP is deliberate: after the upgrade a
websocket is an opaque bidirectional byte stream, and a byte pipe carries it without needing to
understand any of it.
"""
import argparse
import socket
import ssl
import sys
import threading


def pipe(src, dst):
    """Copy until one side closes, then half-close the other so the peer sees the EOF."""
    try:
        while True:
            chunk = src.recv(65536)
            if not chunk:
                break
            dst.sendall(chunk)
    except OSError:
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except OSError:
            pass


def serve_one(tls_conn, target):
    try:
        upstream = socket.create_connection(target)
    except OSError as exc:
        print("terminator: upstream %s:%d unreachable: %s" % (target[0], target[1], exc),
              file=sys.stderr)
        tls_conn.close()
        return
    t = threading.Thread(target=pipe, args=(tls_conn, upstream), daemon=True)
    t.start()
    pipe(upstream, tls_conn)
    t.join()
    upstream.close()
    tls_conn.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--listen", required=True, help="HOST:PORT to accept TLS on")
    ap.add_argument("--target", required=True, help="HOST:PORT of the plain ws:// relay")
    ap.add_argument("--cert", required=True)
    ap.add_argument("--key", required=True)
    args = ap.parse_args()

    lhost, lport = args.listen.rsplit(":", 1)
    thost, tport = args.target.rsplit(":", 1)
    target = (thost, int(tport))

    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.minimum_version = ssl.TLSVersion.TLSv1_2
    ctx.load_cert_chain(args.cert, args.key)

    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((lhost, int(lport)))
    srv.listen(64)
    print("terminator: wss://%s -> ws://%s:%d" % (args.listen, target[0], target[1]), flush=True)

    while True:
        raw, _ = srv.accept()
        try:
            tls_conn = ctx.wrap_socket(raw, server_side=True)
        except (ssl.SSLError, OSError) as exc:
            # A failed handshake is one client's problem, never the terminator's: a phone
            # whose pin no longer matches must not be able to stop the relay serving.
            print("terminator: handshake failed: %s" % exc, file=sys.stderr, flush=True)
            raw.close()
            continue
        threading.Thread(target=serve_one, args=(tls_conn, target), daemon=True).start()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
