# The base image is pinned by digest beside its tag; dependabot moves both.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -tags lambda.norpc \
    -ldflags "-s -w -X main.version=${VERSION}" -o /out/bootstrap .

# aws-lambda-go talks to the lambda runtime api itself, so the image needs nothing but the binary.
FROM scratch
COPY --from=build /out/bootstrap /bootstrap
ENTRYPOINT ["/bootstrap"]
