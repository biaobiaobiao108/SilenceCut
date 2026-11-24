#include "ffmpeg.h"

#include <errno.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/hwcontext.h>
#include <libswresample/swresample.h>
#include "input.h"

struct CutCancel {
    atomic_int canceled;
};

CutCancel *cut_cancel_new(void) {
    CutCancel *cancel = av_malloc(sizeof(*cancel));
    if (cancel) atomic_init(&cancel->canceled, 0);
    return cancel;
}

void cut_cancel_set(CutCancel *cancel) {
    atomic_store_explicit(&cancel->canceled, 1, memory_order_relaxed);
}

void cut_cancel_free(CutCancel *cancel) {
    av_free(cancel);
}

static int cut_interrupted(void *opaque) {
    CutCancel *cancel = opaque;
    return atomic_load_explicit(&cancel->canceled, memory_order_relaxed);
}

static enum AVPixelFormat choose_format(AVCodecContext *ctx, const enum AVPixelFormat *formats) {
    CutInput *in = ctx->opaque;
    for (const enum AVPixelFormat *f = formats; *f != AV_PIX_FMT_NONE; f++)
        if (*f == in->hardware_format) return *f;
    return avcodec_default_get_format(ctx, formats);
}

static void enable_hardware_decoder(const AVCodec *codec, AVCodecContext *ctx, CutInput *in) {
#ifdef _WIN32
    enum AVHWDeviceType type = AV_HWDEVICE_TYPE_D3D11VA;
#else
    enum AVHWDeviceType type = AV_HWDEVICE_TYPE_VIDEOTOOLBOX;
#endif
    for (int i = 0; ; i++) {
        const AVCodecHWConfig *config = avcodec_get_hw_config(codec, i);
        if (!config) return;
        if (config->device_type != type || !(config->methods & AV_CODEC_HW_CONFIG_METHOD_HW_DEVICE_CTX))
            continue;
        if (av_hwdevice_ctx_create(&ctx->hw_device_ctx, type, NULL, NULL, 0) >= 0) {
            in->hardware_format = config->pix_fmt;
            ctx->opaque = in;
            ctx->get_format = choose_format;
            ctx->extra_hw_frames = 12;
        }
        return;
    }
}

static int open_decoder(AVStream *stream, AVCodecContext **ctx, CutInput *in, int hardware) {
    const AVCodec *codec = avcodec_find_decoder(stream->codecpar->codec_id);
    if (!codec) return AVERROR_DECODER_NOT_FOUND;
    *ctx = avcodec_alloc_context3(codec);
    if (!*ctx) return AVERROR(ENOMEM);
    int err = avcodec_parameters_to_context(*ctx, stream->codecpar);
    if (err < 0) return err;
    (*ctx)->pkt_timebase = stream->time_base;
    (*ctx)->thread_count = 4;
    if (hardware) enable_hardware_decoder(codec, *ctx, in);
    err = avcodec_open2(*ctx, codec, NULL);
    if (err >= 0 || !(*ctx)->hw_device_ctx) return err;
    avcodec_free_context(ctx);
    in->hardware_format = AV_PIX_FMT_NONE;
    return open_decoder(stream, ctx, in, 0);
}

CutInput *cut_open(const char *path, int mode, CutCancel *cancel, CutInfo *info, int *err) {
    CutInput *in = av_mallocz(sizeof(*in));
    if (!in) {
        *err = AVERROR(ENOMEM);
        return NULL;
    }
    in->active = -1;
    in->mode = mode;
    in->hardware_format = AV_PIX_FMT_NONE;
    in->packet = av_packet_alloc();
    in->decoded = av_frame_alloc();
    in->audio = av_frame_alloc();
    in->last_video = av_frame_alloc();
    in->format = avformat_alloc_context();
    if (!in->packet || !in->decoded || !in->audio || !in->last_video || !in->format) {
        *err = AVERROR(ENOMEM);
        goto fail;
    }
    if (cancel) in->format->interrupt_callback = (AVIOInterruptCB){cut_interrupted, cancel};
    if (cancel && cut_interrupted(cancel)) {
        *err = AVERROR_EXIT;
        goto fail;
    }
    AVDictionary *opts = NULL;
    // The build enables only the demuxers exposed by the app. Keeping the
    // protocol whitelist here prevents a crafted local file from opening a
    // network or device resource.
    av_dict_set(&opts, "protocol_whitelist", "file", 0);
    *err = avformat_open_input(&in->format, path, NULL, &opts);
    av_dict_free(&opts);
    if (*err < 0) goto fail;
    *err = avformat_find_stream_info(in->format, NULL);
    if (*err < 0) goto fail;

    in->audio_index = -1;
    in->video_index = -1;
    for (unsigned i = 0; i < in->format->nb_streams; i++) {
        const AVStream *stream = in->format->streams[i];
        if (stream->codecpar->codec_type == AVMEDIA_TYPE_AUDIO && in->audio_index < 0)
            in->audio_index = i;
        if (stream->codecpar->codec_type == AVMEDIA_TYPE_VIDEO && in->video_index < 0 &&
            !(stream->disposition & AV_DISPOSITION_ATTACHED_PIC))
            in->video_index = i;
    }
    if (in->audio_index < 0) {
        *err = AVERROR_STREAM_NOT_FOUND;
        goto fail;
    }
    AVStream *audio = in->format->streams[in->audio_index];
    *err = open_decoder(audio, &in->decoder[0], in, 0);
    if (*err < 0) goto fail;
    AVCodecContext *codec = in->decoder[0];
    if (codec->sample_rate <= 0 || codec->ch_layout.nb_channels <= 0 ||
        in->format->duration == AV_NOPTS_VALUE || in->format->duration <= 0) {
        *err = AVERROR_INVALIDDATA;
        goto fail;
    }
    in->rate = codec->sample_rate;
    if (codec->ch_layout.order == AV_CHANNEL_ORDER_UNSPEC)
        av_channel_layout_default(&in->layout, codec->ch_layout.nb_channels);
    else {
        *err = av_channel_layout_copy(&in->layout, &codec->ch_layout);
        if (*err < 0) goto fail;
    }
    in->origin = in->format->start_time == AV_NOPTS_VALUE ? 0 : in->format->start_time;
    info->duration = in->format->duration;
    info->rate = in->rate;
    info->channels = in->layout.nb_channels;
    info->video = in->video_index >= 0;
    if (info->video) {
        AVStream *video = in->format->streams[in->video_index];
        info->width = video->codecpar->width;
        info->height = video->codecpar->height;
        if (mode != CUT_AUDIO_ONLY) {
            *err = open_decoder(video, &in->decoder[1], in, mode >= CUT_HARDWARE);
            if (*err < 0) goto fail;
            info->hardware_decoder = in->hardware_format != AV_PIX_FMT_NONE;
        }
    }
    return in;

fail:
    cut_close(in);
    return NULL;
}

void cut_close(CutInput *in) {
    if (!in) return;
    swr_free(&in->convert);
    av_channel_layout_uninit(&in->layout);
    av_channel_layout_uninit(&in->source_layout);
    av_frame_free(&in->last_video);
    av_frame_free(&in->audio);
    av_frame_free(&in->decoded);
    av_packet_free(&in->packet);
    avcodec_free_context(&in->decoder[0]);
    avcodec_free_context(&in->decoder[1]);
    avformat_close_input(&in->format);
    av_free(in);
}

static int seek_input(CutInput *in, int index, int64_t timestamp) {
    AVIOInterruptCB *interrupt = &in->format->interrupt_callback;
    if (interrupt->callback && interrupt->callback(interrupt->opaque)) return AVERROR_EXIT;
    int err = av_seek_frame(in->format, index, timestamp, AVSEEK_FLAG_BACKWARD);
    if (err < 0) return err;
    for (int i = 0; i < 2; i++)
        if (in->decoder[i]) avcodec_flush_buffers(in->decoder[i]);
    swr_free(&in->convert);
    av_frame_unref(in->last_video);
    in->active = -1;
    in->eof = in->drain = 0;
    in->next_pts[0] = in->next_pts[1] = AV_NOPTS_VALUE;
    return 1;
}

int cut_seek_audio(CutInput *in, int64_t position) {
    AVStream *stream = in->format->streams[in->audio_index];
    int64_t timestamp = av_rescale_q(in->origin + position, AV_TIME_BASE_Q, stream->time_base);
    return seek_input(in, in->audio_index, timestamp);
}

void cut_audio_stereo(CutInput *in) {
    av_channel_layout_uninit(&in->layout);
    in->layout = (AVChannelLayout)AV_CHANNEL_LAYOUT_STEREO;
}

int cut_skip_gap(CutInput *in, int64_t after, int64_t before, int initial) {
    // MOV/MP4 can reposition the other tracks to the selected video index entry.
    // Leave room for reordered frames and audio decoder preroll.
    const int64_t preroll = 2 * AV_TIME_BASE;
    if (before - after <= 2 * preroll || in->video_index < 0 ||
        !in->format->pb || !(in->format->pb->seekable & AVIO_SEEKABLE_NORMAL))
        return 0;
    int index = -1;
    int64_t timestamp = in->origin + before - preroll;
    if (strcmp(in->format->iformat->name, "mov,mp4,m4a,3gp,3g2,mj2") == 0) {
        index = in->video_index;
        AVStream *stream = in->format->streams[index];
        int64_t target = av_rescale_q(timestamp, AV_TIME_BASE_Q, stream->time_base);
        const AVIndexEntry *entry = avformat_index_get_entry_from_timestamp(stream, target, AVSEEK_FLAG_BACKWARD);
        if (!entry || !(entry->flags & AVINDEX_KEYFRAME) ||
            entry->timestamp <= av_rescale_q(in->origin + after + preroll, AV_TIME_BASE_Q, stream->time_base))
            return 0;
        timestamp = entry->timestamp;
    } else if (!initial || before <= 5 * AV_TIME_BASE) {
        return 0;
    }
    return seek_input(in, index, timestamp);
}

void cut_error(int code, char *buf, int size) {
    if (code == AVERROR_STREAM_NOT_FOUND) {
        snprintf(buf, size, "没有音轨");
        return;
    }
    av_strerror(code, buf, size);
}

static int decode_next(CutInput *in, CutFrame *frame) {
    av_frame_unref(in->decoded);
    for (;;) {
        if (in->active >= 0) {
            int err = avcodec_receive_frame(in->decoder[in->active], in->decoded);
            if (err >= 0) return in->active + 1;
            if (err != AVERROR(EAGAIN) && err != AVERROR_EOF) {
                if (in->active == 1) frame->video_error = CUT_VIDEO_DECODE_ERROR;
                return err;
            }
            in->active = -1;
        }
        if (!in->eof) {
            int err = av_read_frame(in->format, in->packet);
            if (err == AVERROR_EOF) {
                in->eof = 1;
                continue;
            }
            if (err < 0) return err;
            int stream = in->packet->stream_index;
            int which = stream == in->audio_index ? 0 : stream == in->video_index && in->decoder[1] ? 1 : -1;
            if (which >= 0) {
                err = avcodec_send_packet(in->decoder[which], in->packet);
                in->active = which;
            }
            av_packet_unref(in->packet);
            if (err < 0) {
                if (which == 1) frame->video_error = CUT_VIDEO_DECODE_ERROR;
                return err;
            }
            continue;
        }
        if (in->drain >= 2) return 0;
        int which = in->drain++;
        if (in->decoder[which]) {
            int err = avcodec_send_packet(in->decoder[which], NULL);
            if (err < 0 && err != AVERROR_EOF) {
                if (which == 1) frame->video_error = CUT_VIDEO_DECODE_ERROR;
                return err;
            }
            in->active = which;
        }
    }
}

static int convert_audio(CutInput *in, CutFrame *frame) {
    AVFrame *decoded = in->decoded, *audio = in->audio;
    int err;
    if (!in->convert || decoded->format != in->source_format || decoded->sample_rate != in->source_rate ||
        av_channel_layout_compare(&in->source_layout, &decoded->ch_layout) != 0) {
        swr_free(&in->convert);
        av_channel_layout_uninit(&in->source_layout);
        if ((err = av_channel_layout_copy(&in->source_layout, &decoded->ch_layout)) < 0) return err;
        in->source_format = decoded->format;
        in->source_rate = decoded->sample_rate;
        err = swr_alloc_set_opts2(&in->convert, &in->layout, AV_SAMPLE_FMT_FLT, in->rate,
                                  &decoded->ch_layout, decoded->format, decoded->sample_rate, 0, NULL);
        if (err < 0) return err;
        if ((err = swr_init(in->convert)) < 0) return err;
    }
    av_frame_unref(audio);
    audio->format = AV_SAMPLE_FMT_FLT;
    audio->sample_rate = in->rate;
    audio->pts = frame->pts;
    audio->time_base = AV_TIME_BASE_Q;
    audio->nb_samples = swr_get_out_samples(in->convert, decoded->nb_samples);
    if (audio->nb_samples <= 0) return AVERROR_INVALIDDATA;
    if ((err = av_channel_layout_copy(&audio->ch_layout, &in->layout)) < 0) return err;
    if ((err = av_frame_get_buffer(audio, 0)) < 0) return err;
    err = swr_convert(in->convert, audio->data, audio->nb_samples,
                      (const uint8_t **)decoded->extended_data, decoded->nb_samples);
    if (err < 0) return err;
    frame->audio = (const float *)audio->data[0];
    frame->samples = audio->nb_samples = err;
    return 1;
}

int cut_next(CutInput *in, CutFrame *frame) {
    int which = decode_next(in, frame);
    if (which <= 0) return which;
    int64_t pts = in->decoded->best_effort_timestamp;
    int index = which == 1 ? in->audio_index : in->video_index;
    AVRational base = in->format->streams[index]->time_base;
    if (pts == AV_NOPTS_VALUE && in->next_pts[which - 1] == AV_NOPTS_VALUE) {
        frame->seek_error = 1;
        return AVERROR_INVALIDDATA;
    }
    frame->pts = pts == AV_NOPTS_VALUE ? in->next_pts[which - 1] : av_rescale_q(pts, base, AV_TIME_BASE_Q) - in->origin;
    if (which == 2) {
        in->next_pts[1] = frame->pts + av_rescale_q(in->decoded->duration, base, AV_TIME_BASE_Q);
        return 2;
    }
    int err = convert_audio(in, frame);
    if (err < 0) return err;
    in->next_pts[0] = frame->pts + av_rescale_q(frame->samples, (AVRational){1, in->rate}, AV_TIME_BASE_Q);
    return 1;
}

int cut_hold_video(CutInput *in) {
    av_frame_unref(in->last_video);
    return av_frame_ref(in->last_video, in->decoded);
}
