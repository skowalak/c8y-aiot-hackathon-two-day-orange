FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags="-s -w -X main.Version=$VERSION" \
    -o /out/simulatord .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/simulatord /simulatord

# The platform probes /health on port 80 and restarts anything that does not
# answer, so the simulator serves one. Credentials arrive as C8Y_BASEURL,
# C8Y_TENANT, C8Y_USER and C8Y_PASSWORD injected by Cumulocity for the
# subscribed tenant's service user; nothing is baked into the image.
#
# REST rather than MQTT: in the cluster C8Y_BASEURL points at an internal
# address with no broker behind it, and MQTT would need device credentials from
# a bulk registration that cannot be handed to a running container.
#
# -mode both is safe to restart: the backfill skips devices that already hold
# measurements in the target window.
ENV SIM_LISTEN=:80 \
    SIM_MODE=both \
    SIM_TRANSPORT=rest \
    SIM_FAULT=-2h \
    SIM_HISTORY=7d
EXPOSE 80
ENTRYPOINT ["/simulatord"]
