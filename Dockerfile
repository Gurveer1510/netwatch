FROM golang:1.26.4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/simulator ./cmd/simulator && \
    CGO_ENABLED=0 go build -trimpath -o /out/collector ./cmd/collector

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl iputils-ping && \
    rm -rf /var/lib/apt/lists/* && useradd --uid 10001 --create-home netwatch
COPY --from=build /out/ /app/
USER netwatch
WORKDIR /app
CMD ["/app/server"]
