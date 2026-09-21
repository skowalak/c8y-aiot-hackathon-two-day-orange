# Multi-stage build for the c8y diagnostic-agent Nitro microservice.
#
# Note: `nitro build` (via c8y-nitro) can also generate its own Docker image +
# cumulocity.json + deployable zip. This standalone Dockerfile is provided for
# environments where you want to build the container directly.

# ---- build stage -----------------------------------------------------------
FROM node:24-slim AS build
WORKDIR /app

# Install deps first for layer caching.
COPY package.json package-lock.json ./
RUN npm ci

# Build the Nitro server output (.output/).
#
# c8y-nitro runs its own zip/Docker packaging on Nitro's "close" hook AFTER the
# server bundle is already written to .output/. That packaging step shells out to
# `docker`, which isn't available inside this build — but by then .output/ is
# complete and is all the runtime stage needs. So we run the build and treat a
# failure in the trailing packaging step as non-fatal, then assert the server
# bundle exists.
COPY . .
RUN npm run build || true \
    && test -f .output/server/index.mjs

# ---- runtime stage ---------------------------------------------------------
FROM node:24-slim AS runtime
WORKDIR /app
ENV NODE_ENV=production

# The Nitro node-server preset bundles its own dependencies into .output, so we
# only need the built server directory.
COPY --from=build /app/.output ./.output

# Cumulocity microservices listen on the port given by the platform; Nitro's
# node-server honours PORT (default 3000).
ENV PORT=80
EXPOSE 80

# ANTHROPIC_API_KEY and (in dev) C8Y_* come from the environment / tenant
# options — never bake secrets into the image.
CMD ["node", ".output/server/index.mjs"]
