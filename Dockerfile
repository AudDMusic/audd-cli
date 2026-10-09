# syntax=docker/dockerfile:1
# The audd CLI in a minimal image: no shell, no ffmpeg (so no --at clips and
# no audd listen). Mount a folder to recognize files in it:
#   docker run --rm -e AUDD_API_TOKEN -v "$PWD:/work" ghcr.io/auddmusic/audd-cli recognize song.mp3

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.1.0
ARG COMMIT=none
ARG DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X github.com/AudDMusic/audd-cli/internal/app.Version=${VERSION} -X github.com/AudDMusic/audd-cli/internal/app.Commit=${COMMIT} -X github.com/AudDMusic/audd-cli/internal/app.Date=${DATE}" \
      -o /out/audd ./cmd/audd

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="audd" \
      org.opencontainers.image.description="Command-line tool for AudD music recognition" \
      org.opencontainers.image.source="https://github.com/AudDMusic/audd-cli" \
      org.opencontainers.image.url="https://audd.io" \
      org.opencontainers.image.vendor="AudD, LLC" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/audd /usr/local/bin/audd
# audd update reads this and prints the docker pull command instead of
# trying to replace the binary inside the container.
ENV AUDD_INSTALL_METHOD=docker
# Config, cache, jobs, and the stream database live under /home/nonroot;
# mount a volume there to keep them between runs.
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/audd"]
CMD ["--help"]
