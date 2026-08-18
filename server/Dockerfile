# Multi-target build: `docker build --target relay` or `--target publicserver`.
# Two separate images from one Dockerfile so the trust boundary in
# docs/DESIGN.md §2.3/§5 stays real at the deployment level too -- the
# publicserver image contains no relay code, no token-checking logic, nothing
# that could serve /mcp or /agent even if misconfigured.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/relay ./cmd/relay
RUN CGO_ENABLED=0 go build -o /out/publicserver ./cmd/publicserver

FROM alpine:3.20 AS relay
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
COPY --from=build /out/relay /usr/local/bin/relay
EXPOSE 8080
USER appuser
ENTRYPOINT ["/usr/local/bin/relay"]

FROM alpine:3.20 AS publicserver
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
COPY --from=build /out/publicserver /usr/local/bin/publicserver
RUN mkdir -p /var/cache/rish-mcp && chown appuser:appuser /var/cache/rish-mcp
EXPOSE 8080
USER appuser
ENTRYPOINT ["/usr/local/bin/publicserver"]
