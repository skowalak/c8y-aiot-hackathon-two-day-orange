FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags="-s -w -X github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/server.Version=$VERSION" \
    -o /out/mcpd .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mcpd /mcpd

# Cumulocity routes microservice traffic to port 80 and mounts the service at
# /service/<name>, which the server needs to know for the report links it hands
# out and for the endpoint its SSE transport advertises.
ENV MCP_ADDR=:80 \
    APPLICATION_NAME=diagnostic-agent-2
EXPOSE 80
ENTRYPOINT ["/mcpd"]
