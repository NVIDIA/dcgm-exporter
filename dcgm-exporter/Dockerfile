FROM ubuntu:16.04

ARG DCGM_VERSION

RUN apt-get update && apt-get install -y --no-install-recommends libgomp1 \
    ca-certificates wget && \
    rm -rf /var/lib/apt/lists/*

RUN wget https://developer.download.nvidia.com/compute/redist/dcgm/${DCGM_VERSION}/DEBS/datacenter-gpu-manager_${DCGM_VERSION}_amd64.deb && \
    dpkg -i datacenter-gpu-manager_*.deb && \
    rm -f datacenter-gpu-manager_*.deb

COPY dcgm-exporter /usr/local/bin

ENV NVIDIA_VISIBLE_DEVICES all
ENV NVIDIA_DRIVER_CAPABILITIES all

VOLUME /run/prometheus

ENTRYPOINT [ "dcgm-exporter", "-e" ]
