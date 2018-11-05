FROM ubuntu:16.04

ARG DCGM_VERSION=1.4.6

COPY datacenter-gpu-manager_${DCGM_VERSION}_amd64.deb /tmp
RUN dpkg -i /tmp/*.deb && rm -f /tmp/*

COPY dcgm-exporter /usr/local/bin

ENV NVIDIA_VISIBLE_DEVICES all
ENV NVIDIA_DRIVER_CAPABILITIES utility

VOLUME /run/prometheus

ENTRYPOINT [ "dcgm-exporter", "-e" ]
