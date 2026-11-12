# Base images are pinned by digest beside their tags; dependabot moves both.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
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
FROM golang:1.27.3-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS go

# uv, to install the dependencies of python functions for lambda's platform.
FROM ghcr.io/astral-sh/uv:0.13.8@sha256:5cc1805e2426484abca4ad46ea783bab281fdb225fd82d8ce6d66a81254bac85 AS uv

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
