#!/bin/sh
# Runs the tests, then builds every target into dist/:
#   windows-x64/goterm.exe   windows-x86/goterm.exe
#   linux-x64/goterm  linux-x64/goterm-cli   linux-x86/goterm  linux-x86/goterm-cli
set -e
go test ./...
rm -rf dist
export CGO_ENABLED=0
for a in amd64:x64 386:x86; do
  arch=${a%%:*}; dir=${a##*:}
  mkdir -p dist/windows-$dir dist/linux-$dir
  env GOOS=windows GOARCH=$arch go build -trimpath -ldflags "-s -w -H windowsgui" -o dist/windows-$dir/goterm.exe .
  env GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w" -o dist/linux-$dir/goterm .
  env GOOS=linux GOARCH=$arch go build -tags cli -trimpath -ldflags "-s -w" -o dist/linux-$dir/goterm-cli .
done
ls -l dist/*/*
