#!/bin/bash -eux

export GO111MODULE=on

echo "Building for $TARGETPLATFORM"
case $TARGETPLATFORM in
  linux/amd64)
    GOARCH=amd64 go build -o /tmp/simplimqtt .
    ;;

  linux/arm/v7)
    GOARM=6 GOARCH=arm go build -o /tmp/simplimqtt .
    ;;

  linux/arm64)
    GOARCH=arm64 go build -o /tmp/simplimqtt .
    ;;

  *)
    echo "Unknown architecture" $TARGETPLATFORM
    exit 1
    ;;
esac
