# =============================================================================
# Project Sentinel — Frontend Dockerfile
# Multi-stage build: Node.js build → nginx static serve
# =============================================================================

# ── Stage 1: Build ──────────────────────────────────────────
FROM node:20-alpine AS builder

WORKDIR /app

# Install dependencies (cache layer)
COPY package.json package-lock.json* ./
RUN npm ci --prefer-offline

# Copy source and build
COPY . .
RUN npm run build

# ── Stage 2: Serve with nginx ─────────────────────────────
FROM nginx:1.27-alpine

# Remove default config
RUN rm /etc/nginx/conf.d/default.conf

# Shipped as a TEMPLATE, not a final config: the nginx image's entrypoint runs
# envsubst over /etc/nginx/templates/*.template and writes the result into
# conf.d/. That is what lets the API upstream differ between compose
# (api:8080) and Kubernetes (sentinel-api:8080) without a second config file.
COPY nginx.conf /etc/nginx/templates/sentinel.conf.template

# Defaults target docker-compose; Kubernetes overrides both via env.
ENV API_UPSTREAM=api:8080 \
    DNS_RESOLVER=127.0.0.11

# Copy built assets from builder
COPY --from=builder /app/dist /usr/share/nginx/html

EXPOSE 80

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1/nginx-health || exit 1

CMD ["nginx", "-g", "daemon off;"]
