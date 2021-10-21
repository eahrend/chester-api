# syntax=docker/dockerfile:experimental

# Build Image
ARG GO_VERSION=1.16
FROM golang:${GO_VERSION}-alpine AS builder
RUN apk add --no-cache --update \
        openssh-client \
        git \
        ca-certificates \
        build-base
RUN mkdir -p /go/src/github.com/eahrend/chester-api
WORKDIR /go/src/github.com/eahrend/chester-api
COPY ./ ./
RUN go mod download
RUN CGO_ENABLED=0 go build \
    -installsuffix 'static' \
    -o /app .

# Application layer
FROM alpine
RUN apk update && apk add ca-certificates && rm -rf /var/cache/apk/*
RUN update-ca-certificates
RUN apk add bash
RUN mkdir /app
COPY --from=builder /app /app
EXPOSE 8080
WORKDIR /app
CMD ["./app"]
