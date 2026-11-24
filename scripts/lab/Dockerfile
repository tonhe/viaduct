# Image for running the network lab without root on the host:
#   make lab-docker
# The lab itself needs root inside the container (network namespaces, nftables,
# raw sockets), so the container runs privileged. It has no route out of its
# own namespaces and sends nothing outside them.
FROM ubuntu:24.04
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 iputils-ping mtr-tiny nftables tcpdump procps \
 && rm -rf /var/lib/apt/lists/*
