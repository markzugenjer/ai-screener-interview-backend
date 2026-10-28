# Builds the Go API and worker into one small image. Run either with:
#   docker run ... ai-screener-backend /app/api
#   docker run ... ai-screener-backend /app/worker
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
COPY . .
# go.sum is created by `go mod tidy`; commit it so builds are reproducible.
RUN [ -f go.sum ] || go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/admin ./cmd/admin

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/ /app/
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT []
CMD ["/app/api"]
