# The image holds the kasad binary and nothing else: no shell, no libraries.
#
#   docker build -t kasad .
#   docker run -e KASAD_HOST=192.0.2.10 -e TZ=Europe/Lisbon -p 8080:8080 -v kasad-data:/data kasad

ARG GO_VERSION=1.27

# Go builds for other kinds of machine by itself, so the build always runs on
# the builder's own kind, whatever the image is for.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=unknown
WORKDIR /src
COPY . .
# No cgo, so the binary needs no libraries. The timetzdata tag builds in the
# timezone database, since the image has no copy of its own. For 32-bit ARM,
# TARGETVARIANT is v6 or v7, and GOARM wants the number.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -tags timetzdata -ldflags "-s -w -X main.version=${VERSION}" -o /out/kasad ./cmd/kasad \
 && mkdir /out/data

FROM scratch
LABEL org.opencontainers.image.title="kasad" \
      org.opencontainers.image.description="Offline control for TP-Link Kasa smart plugs" \
      org.opencontainers.image.source="https://github.com/gureetk/kasad" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/kasad /kasad
# The data directory belongs to the user kasad runs as. A named volume
# mounted here starts out as a copy of it, owner included.
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
# Where kasad keeps its files, and where it offers the web app and the API.
# Rules run by local time, which in a container is UTC unless TZ is set.
ENV KASAD_DATA=/data \
    KASAD_LISTEN=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/kasad"]
CMD ["serve"]
