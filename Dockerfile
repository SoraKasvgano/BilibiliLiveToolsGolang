FROM debian:bookworm-slim

WORKDIR /app

ARG APP_NAME=BilibiliLiveToolsGover

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates tzdata \
  && rm -rf /var/lib/apt/lists/*

COPY dist/${APP_NAME}_linux_amd64 /app/gover
COPY ffmpeg/linux-x64/ffmpeg /app/ffmpeg/linux-x64/ffmpeg

RUN chmod +x /app/gover /app/ffmpeg/linux-x64/ffmpeg \
  && mkdir -p /app/data /app/ffmpeg \
  && ln -sf /app/ffmpeg/linux-x64/ffmpeg /app/ffmpeg/ffmpeg

ENV GOVER_CONFIG_FILE=/app/data/config.json
ENV GOVER_FFMPEG_PATH=/app/ffmpeg/linux-x64/ffmpeg

EXPOSE 18686

VOLUME ["/app/data", "/app/ffmpeg"]

ENTRYPOINT ["/app/gover"]
