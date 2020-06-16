FROM --platform=$BUILDPLATFORM golang:1.14.4 AS builder
ARG TARGETPLATFORM
ARG BUILDPLATFORM

COPY . /src/
WORKDIR /src
RUN ./build-multiarch.sh

FROM debian:buster-slim
RUN apt-get update && apt-get install -y ca-certificates
COPY --from=builder /tmp/simplimqtt /usr/bin/simplimqtt
