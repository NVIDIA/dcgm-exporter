FROM golang:1.11 as build

WORKDIR /go/src/pod-devices-exporter
COPY src .

RUN go install -v pod-devices-exporter

FROM debian:stretch-slim

COPY --from=build /go/bin/pod-devices-exporter /usr/bin/pod-devices-exporter

ENTRYPOINT ["pod-devices-exporter", "-logtostderr"]
