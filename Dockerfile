# Base images are pinned by digest beside their tags; dependabot moves both.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /kontrolplane

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
ARG COMMIT
ARG DATE
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/kontrolplane/koldstart/internal/version.buildVersion=${VERSION} -X github.com/kontrolplane/koldstart/internal/version.buildCommit=${COMMIT} -X github.com/kontrolplane/koldstart/internal/version.buildDate=${DATE}" \
    -o /out/koldstart ./cmd/koldstart

# alpine rather than distroless, for a shell in ci. koldstart downloads the pkl it pins on first
# use, into the cache of the user it runs as.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
COPY --from=build /out/koldstart /usr/local/bin/koldstart
RUN ln -s koldstart /usr/local/bin/kdst && adduser -D -u 10001 koldstart
USER koldstart
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/koldstart"]
