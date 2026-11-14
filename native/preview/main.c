// Sparse, in-memory seek previews. stdout is framed JSON + JPEG, never files.
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/display.h>
#include <libavutil/time.h>
#include <libswscale/swscale.h>
#include <math.h>
#include <stdint.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>

#define COUNT 101
#define MAX_IMAGE (256 * 1024)

static int64_t deadline;
static int interrupted(void *unused) {
    (void)unused;
    return av_gettime_relative() > deadline;
}
static int record(const char *json, const uint8_t *image, int length) {
    uint32_t n = (uint32_t)strlen(json), m = (uint32_t)length;
    uint8_t header[8] = {n >> 24, n >> 16, n >> 8, n,
                         m >> 24, m >> 16, m >> 8, m};
    if (length < 0 || length > MAX_IMAGE) return -1;
    if (fwrite(header, 1, 8, stdout) != 8 || fwrite(json, 1, n, stdout) != n ||
        (m && fwrite(image, 1, m, stdout) != m)) return -1;
    return fflush(stdout);
}

// Seek near a keyframe, then take the first displayable frame. No accurate-seek
// decoding of all intervening frames and no scanning of the complete movie.
static int capture(AVFormatContext *input, AVCodecContext *decoder, int stream,
                   int64_t timestamp, AVFrame *frame, AVPacket *packet) {
    deadline = av_gettime_relative() + 3000000;
    av_frame_unref(frame);
    if (av_seek_frame(input, stream, timestamp, AVSEEK_FLAG_BACKWARD) < 0) return -1;
    avcodec_flush_buffers(decoder);
    for (int i = 0; i < 4096 && !interrupted(NULL); i++) {
        int ret = av_read_frame(input, packet);
        if (ret < 0) {
            avcodec_send_packet(decoder, NULL);
            return avcodec_receive_frame(decoder, frame);
        }
        if (packet->stream_index != stream) {
            av_packet_unref(packet);
            continue;
        }
        ret = avcodec_send_packet(decoder, packet);
        // receive_frame is drained after every packet, so EAGAIN here is an
        // unexpected state, not a reason to silently drop an input packet.
        av_packet_unref(packet);
        if (ret < 0) return ret;
        ret = avcodec_receive_frame(decoder, frame);
        if (ret == 0) return 0;
        if (ret != AVERROR(EAGAIN)) return ret;
    }
    return -1;
}

int main(int argc, char **argv) {
    if (argc == 2 && !strcmp(argv[1], "--version")) {
        puts("openlist-preview/1");
        return 0;
    }
    if (argc != 3) return 2;
    char *end = NULL;
    long resume = strtol(argv[2], &end, 10);
    if (!end || *end || resume < 0 || resume >= COUNT) return 2;
    (void)setpriority(PRIO_PROCESS, 0, 10);
    av_log_set_level(AV_LOG_ERROR);
    deadline = av_gettime_relative() + 5000000;
    int result = 1, stream = -1, rotation = 0;
    AVFormatContext *input = avformat_alloc_context();
    AVCodecContext *decoder = NULL, *encoder = NULL;
    AVFrame *frame = av_frame_alloc(), *small = av_frame_alloc();
    AVPacket *packet = av_packet_alloc(), *jpeg = av_packet_alloc();
    struct SwsContext *scaler = NULL;
    AVDictionary *options = NULL;
    if (!input || !frame || !small || !packet || !jpeg) goto done;
    input->interrupt_callback = (AVIOInterruptCB){interrupted, NULL};
    av_dict_set(&options, "protocol_whitelist", "file", 0);
    // Exclude playlists/concat/image sequences which can open unrelated files.
    av_dict_set(&options, "format_whitelist", "mov,matroska,avi,mpegts,mpeg,flv,ogg,asf", 0);
    if (avformat_open_input(&input, argv[1], NULL, &options) < 0) goto done;
    av_dict_free(&options);
    if (avformat_find_stream_info(input, NULL) < 0) goto done;
    const AVCodec *codec = NULL;
    stream = av_find_best_stream(input, AVMEDIA_TYPE_VIDEO, -1, -1, &codec, 0);
    if (stream < 0 || !codec || input->duration <= 0) goto done;
    AVStream *video = input->streams[stream];
    if (video->disposition & AV_DISPOSITION_ATTACHED_PIC) goto done;
    double duration = input->duration / (double)AV_TIME_BASE;
    if (!isfinite(duration) || duration <= 0) goto done;
    decoder = avcodec_alloc_context3(codec);
    if (!decoder || avcodec_parameters_to_context(decoder, video->codecpar) < 0) goto done;
    decoder->thread_count = 1;
    decoder->max_pixels = 8192LL * 4320;
    decoder->pkt_timebase = video->time_base;
    if (decoder->width <= 0 || decoder->height <= 0 ||
        (int64_t)decoder->width * decoder->height > decoder->max_pixels) goto done;
    if (avcodec_open2(decoder, codec, NULL) < 0) goto done;
    const AVPacketSideData *matrix = av_packet_side_data_get(
        video->codecpar->coded_side_data, video->codecpar->nb_coded_side_data,
        AV_PKT_DATA_DISPLAYMATRIX);
    if (matrix && matrix->size >= 9 * sizeof(int32_t)) {
        double angle = -av_display_rotation_get((const int32_t *)matrix->data);
        if (isfinite(angle)) rotation = ((int)lround(angle / 90) * 90 % 360 + 360) % 360;
    }
    AVRational sar = av_guess_sample_aspect_ratio(input, video, NULL);
    double aspect = decoder->width / (double)decoder->height;
    if (sar.num > 0 && sar.den > 0) aspect *= av_q2d(sar);
    if (!isfinite(aspect) || aspect < 0.01 || aspect > 100) goto done;
    int rotated = rotation == 90 || rotation == 270;
    double display_aspect = rotated ? 1 / aspect : aspect;
    double display_w = fmin(240, 320 * display_aspect);
    double display_h = display_w / display_aspect;
    double scale = fmin(1, sqrt(60000 / (display_w * display_h)));
    int w = (int)lround((rotated ? display_h : display_w) * scale / 2) * 2;
    int h = (int)lround((rotated ? display_w : display_h) * scale / 2) * 2;
    w = w < 2 ? 2 : w;
    h = h < 2 ? 2 : h;
    encoder = avcodec_alloc_context3(avcodec_find_encoder(AV_CODEC_ID_MJPEG));
    if (!encoder) goto done;
    encoder->width = w;
    encoder->height = h;
    encoder->pix_fmt = AV_PIX_FMT_YUVJ420P;
    encoder->color_range = AVCOL_RANGE_JPEG;
    encoder->time_base = (AVRational){1, 1};
    encoder->thread_count = 1;
    encoder->flags |= AV_CODEC_FLAG_QSCALE;
    encoder->global_quality = 3 * FF_QP2LAMBDA;
    if (avcodec_open2(encoder, encoder->codec, NULL) < 0) goto done;
    small->format = encoder->pix_fmt;
    small->width = w;
    small->height = h;
    small->quality = encoder->global_quality;
    small->color_range = AVCOL_RANGE_JPEG;
    if (av_frame_get_buffer(small, 32) < 0) goto done;
    char json[512];
    snprintf(json, sizeof(json), "{\"kind\":\"meta\",\"duration\":%.6f,\"width\":%d,\"height\":%d,\"rotation\":%d,\"total\":101}",
             duration, w, h, rotation);
    if (record(json, NULL, 0)) goto done;
    int order[COUNT], count = 0, images = 0, seen_count = 0;
    int64_t seen[COUNT];
    for (int i = 0; i < COUNT; i += 5) order[count++] = i;
    for (int i = 0; i < COUNT; i++) if (i % 5) order[count++] = i;
    int64_t start = video->start_time == AV_NOPTS_VALUE ? 0 : video->start_time;
    for (int step = (int)resume; step < COUNT; step++) {
        double sample = fmin(duration * order[step] / 100.0, fmax(0, duration - 0.1));
        int64_t target = start + (int64_t)(sample / av_q2d(video->time_base));
        if (capture(input, decoder, stream, target, frame, packet) < 0) goto done;
        if (frame->best_effort_timestamp == AV_NOPTS_VALUE) goto done;
        double time = (frame->best_effort_timestamp - start) * av_q2d(video->time_base);
        if (!isfinite(time) || time > duration + 1) goto done;
        time = fmax(0, time);
        int duplicate = 0;
        for (int i = 0; i < seen_count; i++)
            if (seen[i] == frame->best_effort_timestamp) duplicate = 1;
        if (duplicate) {
            snprintf(json, sizeof(json), "{\"kind\":\"progress\",\"progress\":%d}", step + 1);
            if (record(json, NULL, 0)) goto done;
            continue;
        }
        seen[seen_count++] = frame->best_effort_timestamp;
        scaler = sws_getCachedContext(scaler, frame->width, frame->height, frame->format,
                                     w, h, encoder->pix_fmt, SWS_BILINEAR, NULL, NULL, NULL);
        if (!scaler || av_frame_make_writable(small) < 0) goto done;
        int colorspace = frame->colorspace == AVCOL_SPC_BT709 ? SWS_CS_ITU709 : SWS_CS_DEFAULT;
        if (frame->colorspace == AVCOL_SPC_BT2020_NCL) colorspace = SWS_CS_BT2020;
        const int *coefficients = sws_getCoefficients(colorspace);
        sws_setColorspaceDetails(scaler, coefficients, frame->color_range == AVCOL_RANGE_JPEG,
                                sws_getCoefficients(SWS_CS_ITU601), 1, 0, 1 << 16, 1 << 16);
        if (sws_scale(scaler, (const uint8_t *const *)frame->data, frame->linesize,
                      0, frame->height, small->data, small->linesize) <= 0) goto done;
        small->pts = step;
        if (avcodec_send_frame(encoder, small) < 0 || avcodec_receive_packet(encoder, jpeg) < 0) goto done;
        snprintf(json, sizeof(json), "{\"kind\":\"frame\",\"progress\":%d,\"sampleTime\":%.6f,\"time\":%.6f}", step + 1, sample, time);
        if (record(json, jpeg->data, jpeg->size)) goto done;
        av_packet_unref(jpeg);
        images++;
    }
    snprintf(json, sizeof(json), "{\"kind\":\"done\",\"bytesRead\":%" PRId64 "}",
             input->pb ? input->pb->bytes_read : 0);
    if (!images || record(json, NULL, 0)) goto done;
    result = 0;
done:
    av_dict_free(&options);
    sws_freeContext(scaler);
    av_packet_free(&packet);
    av_packet_free(&jpeg);
    av_frame_free(&frame);
    av_frame_free(&small);
    avcodec_free_context(&decoder);
    avcodec_free_context(&encoder);
    avformat_close_input(&input);
    if (result) fputs("Preview extraction failed\n", stderr);
    return result;
}
