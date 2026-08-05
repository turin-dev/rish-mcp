# Multi-target build: `docker build --target relay` or `--target publicserver`.
# Two separate images from one Dockerfile so the trust boundary in
# docs/DESIGN.md §2.3/§5 stays real at the deployment level too -- the
# publicserver image contains no relay code, no token-checking logic, nothing
# that could serve /mcp or /agent even if misconfigured.

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/relay ./cmd/relay
RUN CGO_ENABLED=0 go build -o /out/publicserver ./cmd/publicserver

FROM alpine:3.20 AS relay
RUN apk add --no-cache ca-certificates
COPY --from=build /out/relay /usr/local/bin/relay
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/relay"]

FROM alpine:3.20 AS publicserver
RUN apk add --no-cache ca-certificates
COPY --from=build /out/publicserver /usr/local/bin/publicserver
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/publicserver"]
