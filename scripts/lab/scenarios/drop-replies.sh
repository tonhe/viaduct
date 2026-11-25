#!/usr/bin/env bash
# Deterministic reply loss: drop exactly every Nth reply of each type that
# router HOP sends back toward the client.
#
#   drop-replies.sh HOP N [FAMILY]
#     HOP     r1 | r2 | r3 | r3b     the router whose replies are dropped
#     N       drop every Nth reply (N=5 is exactly 20%); 1 drops all of them
#     FAMILY  4 (default) or 6
#
# The rules sit on the next router toward the client, in its forward path, so
# every tool tracing from the client is affected the same way. Each reply type
# has its own pair of named counters in table "lab":
#
#   seen_ttl_icmp / drop_ttl_icmp   Time Exceeded quoting an ICMP probe
#   seen_ttl_udp  / drop_ttl_udp    Time Exceeded quoting a UDP probe
#   seen_ttl_tcp  / drop_ttl_tcp    Time Exceeded quoting a TCP probe
#   seen_echo     / drop_echo       Echo Reply (echo tier, or ping)
#
# seen_* counts every matching reply; drop_* counts the ones dropped. The
# counters are the ground truth the lab tests compare via's numbers against.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=scripts/lab/lib.sh
source "$here/lib.sh"
lab_require_root

hop=${1:?usage: drop-replies.sh HOP N [FAMILY]}
n=${2:?usage: drop-replies.sh HOP N [FAMILY]}
family=${3:-4}

# Where the rules go (the router one step closer to the client) and the
# address HOP uses as the source of its replies to the client.
case "$hop" in
  r1)  echo "r1's replies reach the client directly; use r2, r3 or r3b" >&2; exit 2 ;;
  r2)  at=r1; src4=192.0.2.6;  src6=2001:db8:0:2::2 ;;
  r3)  at=r2; src4=192.0.2.10; src6=2001:db8:0:3::2 ;;
  r3b) at=r2; src4=192.0.2.14; src6=2001:db8:0:4::2 ;;
  *)   echo "unknown hop $hop" >&2; exit 2 ;;
esac

# "numgen inc mod N" counts 0,1,...,N-1 over the packets that reach the rule,
# so comparing with N-1 drops the Nth, 2Nth, ... packet exactly.
last=$((n - 1))

if [[ $family == 4 ]]; then
  # In an ICMPv4 error, the quoted IP header starts 8 bytes into the ICMP
  # message; its protocol field is 9 bytes further on: bit offset (8+9)*8.
  lab_x "$at" nft -f - <<EOF
table ip lab {
  counter seen_ttl_icmp {}
  counter drop_ttl_icmp {}
  counter seen_ttl_udp {}
  counter drop_ttl_udp {}
  counter seen_ttl_tcp {}
  counter drop_ttl_tcp {}
  counter seen_echo {}
  counter drop_echo {}
  chain c_ttl_icmp { counter name seen_ttl_icmp; numgen inc mod $n == $last counter name drop_ttl_icmp drop; }
  chain c_ttl_udp  { counter name seen_ttl_udp;  numgen inc mod $n == $last counter name drop_ttl_udp drop; }
  chain c_ttl_tcp  { counter name seen_ttl_tcp;  numgen inc mod $n == $last counter name drop_ttl_tcp drop; }
  chain c_echo     { counter name seen_echo;     numgen inc mod $n == $last counter name drop_echo drop; }
  chain lab_forward {
    type filter hook forward priority 0; policy accept;
    ip saddr $src4 icmp type time-exceeded @th,136,8 1  jump c_ttl_icmp
    ip saddr $src4 icmp type time-exceeded @th,136,8 17 jump c_ttl_udp
    ip saddr $src4 icmp type time-exceeded @th,136,8 6  jump c_ttl_tcp
    ip saddr $src4 icmp type echo-reply jump c_echo
  }
}
EOF
else
  # In an ICMPv6 error, the quoted IPv6 header starts 8 bytes into the ICMPv6
  # message; its next-header field is 6 bytes further on: bit offset (8+6)*8.
  lab_x "$at" nft -f - <<EOF
table ip6 lab {
  counter seen_ttl_icmp {}
  counter drop_ttl_icmp {}
  counter seen_ttl_udp {}
  counter drop_ttl_udp {}
  counter seen_ttl_tcp {}
  counter drop_ttl_tcp {}
  counter seen_echo {}
  counter drop_echo {}
  chain c_ttl_icmp { counter name seen_ttl_icmp; numgen inc mod $n == $last counter name drop_ttl_icmp drop; }
  chain c_ttl_udp  { counter name seen_ttl_udp;  numgen inc mod $n == $last counter name drop_ttl_udp drop; }
  chain c_ttl_tcp  { counter name seen_ttl_tcp;  numgen inc mod $n == $last counter name drop_ttl_tcp drop; }
  chain c_echo     { counter name seen_echo;     numgen inc mod $n == $last counter name drop_echo drop; }
  chain lab_forward {
    type filter hook forward priority 0; policy accept;
    ip6 saddr $src6 icmpv6 type time-exceeded @th,112,8 58 jump c_ttl_icmp
    ip6 saddr $src6 icmpv6 type time-exceeded @th,112,8 17 jump c_ttl_udp
    ip6 saddr $src6 icmpv6 type time-exceeded @th,112,8 6  jump c_ttl_tcp
    ip6 saddr $src6 icmpv6 type echo-reply jump c_echo
  }
}
EOF
fi
echo "dropping every ${n}th reply from $hop (IPv$family), rules on $at"
