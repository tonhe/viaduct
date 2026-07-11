#!/usr/bin/env bash
# scripts/capture-references.sh
#
# Helper for Tier 4.3: capture Wireshark-verified reference packets to
# replace self-referential expected-values in checksum / parser tests.
#
# What this does:
#   1. Starts tcpdump capturing on the given interface
#   2. Runs a single via trace against a well-known target
#   3. Stops tcpdump
#   4. Prints extraction instructions for the captured pcap
#
# Usage:
#   sudo bash scripts/capture-references.sh <interface> [target]
#   sudo bash scripts/capture-references.sh en0
#   sudo bash scripts/capture-references.sh eth0 dns.google
#
# Requires: tcpdump, tshark (part of Wireshark), sudo, via installed.

set -euo pipefail

IFACE="${1:?usage: $0 <interface> [target]}"
TARGET="${2:-1.1.1.1}"
OUT="/tmp/via-refcapture-$(date +%Y%m%d-%H%M%S).pcap"

echo "==> Capturing on $IFACE, target=$TARGET, output=$OUT"
echo "==> Filter: icmp or (tcp and port 33434) or (udp and portrange 33434-33534)"

# Start tcpdump in background
tcpdump -i "$IFACE" -w "$OUT" \
  '(icmp or (tcp and port 33434) or (udp and portrange 33434-33534))' &
TCPDUMP_PID=$!

# Give tcpdump a moment to start
sleep 1

echo "==> Running via -r -c 1 -P tcp $TARGET"
via -r -c 1 -P tcp "$TARGET" || true

echo "==> Running via -r -c 1 -P udp $TARGET"
via -r -c 1 -P udp "$TARGET" || true

echo "==> Running via -r -c 1 -P icmp $TARGET"
via -r -c 1 -P icmp "$TARGET" || true

# Stop tcpdump
sleep 1
kill -INT "$TCPDUMP_PID" || true
wait "$TCPDUMP_PID" 2>/dev/null || true

echo
echo "==> Capture complete: $OUT"
echo
echo "-------------------------------------------------------------------------"
echo "Extraction instructions"
echo "-------------------------------------------------------------------------"
echo
echo "# 1. List the captured frames:"
echo "tshark -r $OUT -T fields -e frame.number -e ip.src -e ip.dst \\"
echo "  -e ip.proto -e tcp.srcport -e tcp.dstport -e udp.srcport -e udp.dstport"
echo
echo "# 2. Dump one TCP SYN sent by via (adjust frame N):"
echo "tshark -r $OUT -Y 'frame.number == N' -x"
echo
echo "# 3. Dump one ICMP TimeExceeded response inner-header:"
echo "tshark -r $OUT -Y 'icmp.type == 11 and frame.number == N' -x"
echo
echo "# 4. Copy the hex bytes into test fixtures:"
echo "   - internal/probe/family_test.go :: TestParseInnerHeader_IPv4_TCP"
echo "   - internal/probe/family_test.go :: TestTCPChecksum_IPv4"
echo "   - Look for the 'WIRESHARK-REPLACE-ME' markers."
echo
echo "# 5. Update comments to cite the capture:"
echo "   // Reference bytes captured from real via output on YYYY-MM-DD,"
echo "   // verified via Wireshark from $OUT."
echo
