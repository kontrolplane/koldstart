# Base images are pinned by digest beside their tags; dependabot moves both.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /kontrolplane

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY hack/ hack/
COPY internal/ internal/
COPY pkl/ pkl/

ARG TARGETOS
ARG TARGETARCH
ARG VERSION
ARG COMMIT
ARG DATE
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X github.com/kontrolplane/koldstart/internal/version.buildVersion=${VERSION} -X github.com/kontrolplane/koldstart/internal/version.buildCommit=${COMMIT} -X github.com/kontrolplane/koldstart/internal/version.buildDate=${DATE}" \
    -o /out/koldstart ./cmd/koldstart
RUN go run ./hack/pkl -os "$TARGETOS" -arch "$TARGETARCH" -out /out/pkl

# Go for the platform of the image, to build go functions in it.
FROM golang:1.27.3-trixie@sha256:ce01baa8d47f3917e015fd28dad23b42b7e2332070bf85d8ae3e9c2f035ef2da AS go

# uv, to install the dependencies of python functions for lambda's platform.
FROM ghcr.io/astral-sh/uv:0.12.18@sha256:3adc3706091ce7c2fe595e669628caedd6d951551b92b258b7e7dbe06d9440bc AS uv

# debian rather than alpine, as pkl has no build for musl on arm64, and rather than distroless,
# for a shell in ci. it runs as root, so a checkout mounted into it is writable; locally, pass
# --user "$(id -u):$(id -g)" to keep the files yours. HOME is /tmp, writable by any user, for the
# caches of go and koldstart.
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
COPY --from=go /usr/local/go /usr/local/go
COPY --from=go /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=uv /uv /usr/local/bin/uv
COPY --from=build /out/koldstart /usr/local/bin/koldstart
COPY --from=build /out/pkl /usr/local/bin/pkl
RUN ln -s koldstart /usr/local/bin/kdst
ENV PATH=/usr/local/go/bin:$PATH \
    KOLDSTART_PKL=/usr/local/bin/pkl \
    HOME=/tmp
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/koldstart"]
