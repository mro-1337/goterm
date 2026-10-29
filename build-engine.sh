#!/bin/sh
# Rebuilds the Go engine for every Android ABI into app/src/main/jniLibs.
# Run from the android/ folder; needs Go 1.23+ (no NDK required).
set -e
cd "$(dirname "$0")/.."
for t in "arm64 arm64-v8a" "arm armeabi-v7a" "amd64 x86_64" "386 x86"; do
  set -- $t
  env GOOS=linux GOARCH=$1 GOARM=7 CGO_ENABLED=0 \
    go build -tags engine -trimpath -ldflags "-s -w" -o android/app/src/main/jniLibs/$2/libgoterm_engine.so .
done
ls -l android/app/src/main/jniLibs/*/
