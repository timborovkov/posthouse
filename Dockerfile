# syntax=docker/dockerfile:1
FROM golang:1.26.6-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/posthouse ./cmd/posthouse

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -S posthouse \
    && adduser -S -G posthouse -u 10001 posthouse
COPY --from=build /out/posthouse /usr/local/bin/posthouse
RUN mkdir /data && chown posthouse:posthouse /data
USER posthouse
VOLUME ["/data"]
EXPOSE 8791
ENTRYPOINT ["posthouse"]
CMD ["--config", "/data/config.json", "mcp", "http", "--address", "127.0.0.1:8791"]
