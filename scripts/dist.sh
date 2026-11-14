#!/bin/sh
# Builds the files of a release into dist/: an archive for each kind of
# machine, and a list of their checksums.
#
# usage: scripts/dist.sh <version>
#
# The release workflow runs this. It needs only Go, tar and sha256sum, so it
# can also be run by hand to see what a release would hold.
set -eu

version=${1:?usage: scripts/dist.sh <version>}
cd "$(dirname "$0")/.."

rm -rf dist
mkdir -p dist/build

# Each target is os/arch. 32-bit ARM also says which generation of the
# processor to build for: 7 suits a Raspberry Pi 2 or later, 6 the first Pi
# and the Pi Zero.
for target in linux/amd64 linux/arm64 linux/arm/7 linux/arm/6; do
	os=${target%%/*}
	rest=${target#*/}
	arch=${rest%%/*}
	arm=
	case $rest in
	*/*) arm=${rest#*/} ;;
	esac
	name=kasad-$version-$os-$arch${arm:+v$arm}

	mkdir "dist/build/$name"
	# No cgo, so the binary needs nothing from the system it runs on. The
	# timetzdata tag builds in the timezone database, for systems that have
	# none of their own.
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=$arm go build -trimpath -tags timetzdata \
		-ldflags "-s -w -X main.version=$version" -o "dist/build/$name/kasad" ./cmd/kasad
	cp README.md LICENSE contrib/kasad.service contrib/kasad.env.example "dist/build/$name/"
	tar -C dist/build --owner=0 --group=0 --numeric-owner -czf "dist/$name.tar.gz" "$name"
done
rm -rf dist/build

(cd dist && sha256sum -- *.tar.gz >SHA256SUMS)
ls -l dist
