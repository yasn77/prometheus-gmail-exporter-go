# syntax=docker/dockerfile:1

##
## Build
##
FROM golang:1.26-alpine AS build

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY *.go ./

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
    -o /prometheus-gmail-exporter-go

##
## Deploy
##
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /prometheus-gmail-exporter-go /prometheus-gmail-exporter-go

EXPOSE 2112

USER nonroot:nonroot

ENTRYPOINT ["/prometheus-gmail-exporter-go"]
