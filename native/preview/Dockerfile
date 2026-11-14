ARG BASE_IMAGE=openlistteam/openlist-base-image:base

FROM ${BASE_IMAGE} AS preview-build
USER root
RUN apk add --no-cache build-base ffmpeg-dev pkgconf
WORKDIR /src
COPY main.c ./
RUN mkdir -p /out && \
    cc -std=c11 -O2 -Wall -Wextra -Werror main.c \
    $(pkg-config --cflags --libs libavformat libavcodec libavutil libswscale) \
    -lm -o /out/openlist-preview

FROM ${BASE_IMAGE} AS preview-runtime
USER root
COPY --from=preview-build --chmod=755 /out/openlist-preview /opt/openlist/openlist-preview
RUN apk add --no-cache ffmpeg-libavformat ffmpeg-libavcodec ffmpeg-libavutil ffmpeg-libswscale
RUN /opt/openlist/openlist-preview --version
