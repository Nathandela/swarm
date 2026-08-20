#!/bin/bash
# R6 fixture-recording hook relay (the spike-SB relay, reconstructed): forwards the hook's
# stdin JSON to the swarm-char hook sink as one newline-delimited line, teeing a forensic copy.
exec /usr/bin/python3 /tmp/r6rec/relay.py
