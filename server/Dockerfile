FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /yueying .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /yueying .
ENV NATIVE_CLIENT_ORIGINS=http://tauri.localhost,tauri://localhost,https://localhost,capacitor://localhost,https://yueying.localhost
COPY --chmod=755 native-origin-migration.sh /app/native-origin-migration.sh
USER nobody
ENTRYPOINT ["/app/native-origin-migration.sh"]
CMD ["./yueying"]
