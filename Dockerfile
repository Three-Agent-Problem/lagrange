# Lagrange: one image with the server, the tunnel client and the web app.
#   docker build -t lagrange .
#   docker run -p 8080:8080 -e DATABASE_URL=... -e LAGRANGE_PUBLIC_URL=... lagrange

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lagrange ./cmd/lagrange

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/lagrange /usr/local/bin/lagrange
ENV LAGRANGE_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s CMD ["lagrange", "healthcheck"]
ENTRYPOINT ["lagrange"]
CMD ["serve"]
