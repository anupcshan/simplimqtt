#!/bin/bash

. version.sh
docker buildx build --platform linux/arm/v7,linux/arm64,linux/amd64 -t anupcshan/simplimqtt:$VERSION --push .
