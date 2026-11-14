# Native seek previews

The optional `openlist-preview` executable uses FFmpeg libraries to sample a local
video with one demuxer/decoder context. It writes framed JPEG records to stdout;
it never creates thumbnail files. The server owns the queue, authorization,
cancellation and bounded memory cache. The browser decodes images before hover.

Build with FFmpeg 7 or newer development headers and a C11 compiler:

```sh
python3 native/preview/build.py
```

`pkg-config` supplies library flags. On macOS the builder also accepts
`FFMPEG_PREFIX` (default: Homebrew's FFmpeg prefix). Install the result alongside
the OpenList executable, or set `OPENLIST_PREVIEW_HELPER` to its absolute path.
The helper and FFmpeg shared libraries must target the server's operating system.
Missing helpers disable previews without affecting playback.

For Alpine-based container deployments, build the reusable runtime layer with
the same pinned base image used for the server:

```sh
docker build --build-arg BASE_IMAGE=openlistteam/openlist-base-image:base \
  -t openlist-preview-runtime native/preview
```

Use that image as the base of the server's deployment Dockerfile, then copy in
the server executable and accepted frontend. It includes the Linux helper and
its FFmpeg shared libraries; the compiler and development headers stay in the
build stage. The deployment must set its normal non-root user and entrypoint.
Pin `BASE_IMAGE` to a digest for reproducible deployments. This layer does not
replace the normal OpenList image by itself.

Invocation is `openlist-preview ABSOLUTE_FILE COMPLETED_SAMPLE_COUNT`. A run
samples 21 evenly spaced points first, then fills an overall 100-interval grid.
Repeated decoded timestamps emit progress without another JPEG. Resume skips
completed sample positions. Each run is serial, uses one decoder thread and a
lower process priority. Playlist demuxers and network protocols are excluded.

Each record contains two unsigned 32-bit big-endian lengths (JSON, JPEG), then
UTF-8 JSON and JPEG bytes. Metadata precedes frame/progress records; success ends
with `done`. JSON is limited to 1 KiB and a JPEG to 256 KiB. Sample positions and
actual decoded timestamps are separate. Rotation is clockwise in degrees.

`POST /api/fs/preview` accepts the same user-relative `path` and directory
`password` as file browsing. It streams `application/vnd.openlist.preview` with
`Cache-Control: private, no-store`. Disconnect releases a task lease; the last
subscriber leaving cancels the process. Completed images expire after ten idle
minutes, with 8 MiB per video and 64 MiB globally. Clients must not persist them.

Local files are supported independently of the media-library/transcoding flags.
Shared links, remote storage and live sources currently retain the normal time
tooltip. The helper does not implement HDR tone mapping; compare HDR sources
separately before extending the current SDR acceptance.
