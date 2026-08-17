# syntax=docker/dockerfile:1
FROM golang:1.26.6-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG POSTHOUSE_GOOGLE_CLIENT_ID=
ARG POSTHOUSE_GOOGLE_CLIENT_SECRET=
ARG POSTHOUSE_MICROSOFT_CLIENT_ID=
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/timborovkov/posthouse/internal/oauth.GoogleClientID=${POSTHOUSE_GOOGLE_CLIENT_ID} -X github.com/timborovkov/posthouse/internal/oauth.GoogleClientSecret=${POSTHOUSE_GOOGLE_CLIENT_SECRET} -X github.com/timborovkov/posthouse/internal/oauth.MicrosoftClientID=${POSTHOUSE_MICROSOFT_CLIENT_ID}" \
    -o /out/posthouse ./cmd/posthouse

FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget \
    && addgroup -S posthouse \
    && adduser -S -G posthouse -u 10001 posthouse
COPY --from=build /out/posthouse /usr/local/bin/posthouse
RUN mkdir /data && chown posthouse:posthouse /data
USER posthouse
VOLUME ["/data"]
EXPOSE 8791
ENTRYPOINT ["posthouse"]
CMD ["--config", "/data/config.json", "serve"]
