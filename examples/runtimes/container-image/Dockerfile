# The base image is pinned by digest beside its tag; dependabot moves both.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /source

COPY go.mod go.sum ./
RUN go mod download

# .dockerignore leaves the tests out, so a change to them builds no new image.
COPY *.go ./
ARG TARGETARCH
ARG VERSION=0.0.0-development
ARG REVISION=unknown
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -tags lambda.norpc \
    -ldflags "-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" -o /output/bootstrap .

# aws-lambda-go talks to the lambda runtime api itself, so the image needs nothing but the binary.
FROM scratch
ARG VERSION=0.0.0-development
ARG REVISION=unknown
LABEL org.opencontainers.image.title="status" \
      org.opencontainers.image.source="https://github.com/kontrolplane/koldstart/tree/main/examples/runtimes/container-image" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /output/bootstrap /bootstrap
ENTRYPOINT ["/bootstrap"]
