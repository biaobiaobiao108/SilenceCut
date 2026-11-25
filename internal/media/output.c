#include "ffmpeg.h"
#include "input.h"

#include <errno.h>
#include <string.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavutil/audio_fifo.h>
#include <libavutil/cpu.h>
#include <libavutil/hwcontext.h>
#include <libavutil/opt.h>
#include <libavutil/time.h>
#include <libswscale/swscale.h>

struct CutOutput {
    AVFormatContext *format;
    AVCodecContext *audio, *video;
    AVStream *audio_stream, *video_stream;
    AVPacket *packet;
    AVFrame *picture, *download;
    AVFilterGraph *gpu_scale;
    AVFilterContext *gpu_input, *gpu_output;
    const void *gpu_frames;
    int gpu_width, gpu_height;
    AVAudioFifo *fifo;
    SwrContext *resample;
    SwsContext *scale, *tone_map;
    enum AVColorTransferCharacteristic source_transfer;
    int64_t audio_pts, video_pts, video_end, video_final_pts, duration;
    int source_channels;
    int profile_video;
    int64_t readback_us, tone_map_us, video_encode_us;
};

static int add_stream(CutOutput *out, AVCodecContext *codec, AVStream **stream, AVDictionary **opts) {
    if (!codec) return AVERROR_ENCODER_NOT_FOUND;
    *stream = avformat_new_stream(out->format, NULL);
    if (!*stream) return AVERROR(ENOMEM);
    (*stream)->time_base = codec->time_base;
    if (out->format->oformat->flags & AVFMT_GLOBALHEADER) codec->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
    int err = avcodec_open2(codec, codec->codec, opts);
    if (err < 0) return err;
    return avcodec_parameters_from_context((*stream)->codecpar, codec);
}

static int add_audio(CutOutput *out, CutInput *in, int is_video) {
    const AVCodec *codec = avcodec_find_encoder(is_video ? AV_CODEC_ID_AAC : AV_CODEC_ID_PCM_S24LE);
    if (!codec) return AVERROR_ENCODER_NOT_FOUND;
    AVCodecContext *a = out->audio = avcodec_alloc_context3(codec);
    if (!a) return AVERROR(ENOMEM);
    a->sample_rate = in->rate;
    int err;
    if (is_video) {
        const int *rates = NULL;
        int count = 0, supported = 0;
        err = avcodec_get_supported_config(a, codec, AV_CODEC_CONFIG_SAMPLE_RATE, 0, (const void **)&rates, &count);
        if (err < 0) return err;
        for (int i = 0; i < count; i++) supported |= rates[i] == a->sample_rate;
        if (rates && !supported) a->sample_rate = 48000;
        a->sample_fmt = AV_SAMPLE_FMT_FLTP;
        av_channel_layout_default(&a->ch_layout, FFMIN(in->layout.nb_channels, 2));
        a->bit_rate = a->ch_layout.nb_channels == 1 ? 128000 : 192000;
    } else {
        a->sample_fmt = AV_SAMPLE_FMT_S32;
        if ((err = av_channel_layout_copy(&a->ch_layout, &in->layout)) < 0) return err;
    }
    a->time_base = (AVRational){1, a->sample_rate};
    if ((err = add_stream(out, a, &out->audio_stream, NULL)) < 0) return err;
    out->source_channels = in->layout.nb_channels;
    out->fifo = av_audio_fifo_alloc(a->sample_fmt, a->ch_layout.nb_channels, FFMAX(a->frame_size, 4096));
    if (!out->fifo) return AVERROR(ENOMEM);
    err = swr_alloc_set_opts2(&out->resample, &a->ch_layout, a->sample_fmt, a->sample_rate,
                             &in->layout, AV_SAMPLE_FMT_FLT, in->rate, 0, NULL);
    if (err < 0) return err;
    return swr_init(out->resample);
}

static int is_hdr(enum AVColorTransferCharacteristic transfer) {
    return transfer == AVCOL_TRC_SMPTE2084 || transfer == AVCOL_TRC_ARIB_STD_B67;
}

static int add_video(CutOutput *out, CutInput *in) {
    out->profile_video = av_log_get_level() >= AV_LOG_VERBOSE;
#ifdef _WIN32
    const char *name = "h264_mf";
#else
    const char *name = "h264_videotoolbox";
#endif
    const AVCodec *codec = avcodec_find_encoder_by_name(name);
    if (!codec) return AVERROR_ENCODER_NOT_FOUND;
    AVCodecContext *v = out->video = avcodec_alloc_context3(codec);
    if (!v) return AVERROR(ENOMEM);
    const AVCodecContext *source = in->decoder[1];
    AVStream *stream = in->format->streams[in->video_index];
    if (source->width <= 0 || source->height <= 0) return AVERROR_INVALIDDATA;
    int width = source->width, height = source->height;
    v->width = FFMAX(2, (width + 1) & ~1);
    v->height = FFMAX(2, (height + 1) & ~1);
    v->framerate = av_guess_frame_rate(in->format, stream, NULL);
    if (v->framerate.num <= 0 || v->framerate.den <= 0) v->framerate = (AVRational){30, 1};
    v->time_base = (AVRational){1, 90000};
    v->sample_aspect_ratio = av_guess_sample_aspect_ratio(in->format, stream, NULL);
    out->source_transfer = is_hdr(stream->codecpar->color_trc) ? stream->codecpar->color_trc : source->color_trc;
    int hdr = is_hdr(out->source_transfer);
    if (hdr) {
        v->color_primaries = AVCOL_PRI_BT709;
        v->color_trc = AVCOL_TRC_BT709;
        v->colorspace = AVCOL_SPC_BT709;
        v->color_range = AVCOL_RANGE_MPEG;
    } else {
        v->color_primaries = source->color_primaries;
        v->color_trc = source->color_trc;
        v->colorspace = source->colorspace == AVCOL_SPC_RGB ? AVCOL_SPC_BT709 : source->colorspace;
        v->color_range = source->color_range;
    }
    int direct = !hdr && in->mode == CUT_DIRECT && in->hardware_format != AV_PIX_FMT_NONE;
    v->pix_fmt = direct ? in->hardware_format : AV_PIX_FMT_NV12;
    if (direct) v->sw_pix_fmt = AV_PIX_FMT_NV12;
    v->max_b_frames = 0;
    v->gop_size = av_rescale(2, v->framerate.num, v->framerate.den);
    v->bit_rate = av_clip64((int64_t)(v->width * (double)v->height * av_q2d(v->framerate) * .12), 1000000, 50000000);
    AVDictionary *opts = NULL;
#ifdef _WIN32
    if (in->mode >= CUT_HARDWARE_ENCODER) av_dict_set(&opts, "hw_encoding", "1", 0);
#else
    if (in->mode == CUT_SOFTWARE) {
        av_dict_set(&opts, "allow_sw", "1", 0);
        av_dict_set(&opts, "require_sw", "1", 0);
    }
#endif
    int err = add_stream(out, v, &out->video_stream, &opts);
    av_dict_free(&opts);
    if (err < 0) return err;
    out->video_stream->avg_frame_rate = v->framerate;
    out->video_stream->sample_aspect_ratio = v->sample_aspect_ratio;
    const AVPacketSideData *rotation = av_packet_side_data_get(stream->codecpar->coded_side_data,
        stream->codecpar->nb_coded_side_data, AV_PKT_DATA_DISPLAYMATRIX);
    if (rotation) {
        AVCodecParameters *dst = out->video_stream->codecpar;
        AVPacketSideData *copy = av_packet_side_data_new(&dst->coded_side_data, &dst->nb_coded_side_data,
                                                        rotation->type, rotation->size, 0);
        if (!copy) return AVERROR(ENOMEM);
        memcpy(copy->data, rotation->data, rotation->size);
    }
    out->picture = av_frame_alloc();
    if (!out->picture) return AVERROR(ENOMEM);
    out->picture->format = v->pix_fmt;
    out->picture->width = v->width;
    out->picture->height = v->height;
    out->picture->sample_aspect_ratio = v->sample_aspect_ratio;
    return direct ? 0 : av_frame_get_buffer(out->picture, 0);
}

CutOutput *cut_create(CutInput *in, const char *path, int64_t duration, int *err, int *video_error) {
    CutOutput *out = av_mallocz(sizeof(*out));
    if (!out) {
        *err = AVERROR(ENOMEM);
        return NULL;
    }
    out->video_pts = AV_NOPTS_VALUE;
    out->video_final_pts = AV_NOPTS_VALUE;
    out->duration = duration;
    out->packet = av_packet_alloc();
    if (!out->packet) {
        *err = AVERROR(ENOMEM);
        goto fail;
    }
    int video = in->decoder[1] != NULL;
    *err = avformat_alloc_output_context2(&out->format, NULL, video ? "mp4" : "wav", path);
    if (*err < 0) goto fail;
    if (video && (*err = add_video(out, in)) < 0) {
        *video_error = CUT_VIDEO_ENCODE_ERROR;
        goto fail;
    }
    if (video) out->video_end = av_rescale_q(duration, AV_TIME_BASE_Q, out->video->time_base);
    if ((*err = add_audio(out, in, video)) < 0) goto fail;
    if (video) {
        // Millisecond movie ticks can round short AAC edit lists down to zero.
        *err = av_opt_set_int(out->format->priv_data, "movie_timescale", AV_TIME_BASE, 0);
        if (*err < 0) goto fail;
    } else {
        av_opt_set(out->format->priv_data, "rf64", "auto", 0);
    }
    out->format->interrupt_callback = in->format->interrupt_callback;
    if ((*err = avio_open2(&out->format->pb, path, AVIO_FLAG_WRITE,
                           &out->format->interrupt_callback, NULL)) < 0) goto fail;
    AVDictionary *opts = NULL;
    if (video && (out->video->color_primaries != AVCOL_PRI_UNSPECIFIED ||
                  out->video->color_trc != AVCOL_TRC_UNSPECIFIED ||
                  out->video->colorspace != AVCOL_SPC_UNSPECIFIED ||
                  out->video->color_range != AVCOL_RANGE_UNSPECIFIED))
        av_dict_set(&opts, "movflags", "+write_colr", 0);
    *err = avformat_write_header(out->format, &opts);
    av_dict_free(&opts);
    if (*err < 0) goto fail;
    return out;

fail:
    cut_discard(out);
    return NULL;
}

static int encode(CutOutput *out, AVCodecContext *codec, AVStream *stream, AVFrame *frame, int *video_error) {
    if (video_error) *video_error = CUT_VIDEO_ENCODE_ERROR;
    int err = avcodec_send_frame(codec, frame);
    if (err < 0) return err;
    for (;;) {
        err = avcodec_receive_packet(codec, out->packet);
        if (err == AVERROR(EAGAIN) || err == AVERROR_EOF) return 0;
        if (err < 0) return err;
        out->packet->stream_index = stream->index;
        // Native encoders may emit a full-frame duration for a partial final frame.
        if (codec == out->video && out->packet->pts != AV_NOPTS_VALUE) {
            int64_t remaining = FFMAX(1, out->video_end - out->packet->pts);
            if (out->packet->pts == out->video_final_pts || out->packet->duration > remaining)
                out->packet->duration = remaining;
        }
        av_packet_rescale_ts(out->packet, codec->time_base, stream->time_base);
        err = av_interleaved_write_frame(out->format, out->packet);
        if (err < 0) {
            if (video_error) *video_error = 0;
            return err;
        }
    }
}

static int encode_video(CutOutput *out, AVFrame *frame, int *video_error) {
    int64_t started = out->profile_video ? av_gettime_relative() : 0;
    int err = encode(out, out->video, out->video_stream, frame, video_error);
    if (out->profile_video) out->video_encode_us += av_gettime_relative() - started;
    return err;
}

static int write_audio(CutOutput *out, int final) {
    AVCodecContext *a = out->audio;
    int block = a->frame_size ? a->frame_size : 4096;
    for (;;) {
        int available = av_audio_fifo_size(out->fifo);
        if (!available || (!final && available < block)) return 0;
        AVFrame *frame = av_frame_alloc();
        if (!frame) return AVERROR(ENOMEM);
        frame->format = a->sample_fmt;
        frame->sample_rate = a->sample_rate;
        frame->nb_samples = FFMIN(available, block);
        frame->pts = out->audio_pts;
        int err = av_channel_layout_copy(&frame->ch_layout, &a->ch_layout);
        if (err >= 0) err = av_frame_get_buffer(frame, 0);
        if (err >= 0) {
            int read = av_audio_fifo_read(out->fifo, (void **)frame->extended_data, frame->nb_samples);
            err = read == frame->nb_samples ? 0 : AVERROR_INVALIDDATA;
        }
        if (err >= 0) err = encode(out, a, out->audio_stream, frame, NULL);
        out->audio_pts += frame->nb_samples;
        av_frame_free(&frame);
        if (err < 0) return err;
    }
}

static int resample_audio(CutOutput *out, const float *data, int samples) {
    AVCodecContext *a = out->audio;
    int capacity = swr_get_out_samples(out->resample, samples);
    if (capacity <= 0) return capacity;
    uint8_t **planes = NULL;
    int err = av_samples_alloc_array_and_samples(&planes, NULL, a->ch_layout.nb_channels,
                                                 capacity, a->sample_fmt, 0);
    if (err < 0) return err;
    const uint8_t *input[] = {(const uint8_t *)data};
    int count = swr_convert(out->resample, planes, capacity, data ? input : NULL, samples);
    if (count < 0) err = count;
    else if (count > 0) {
        err = av_audio_fifo_write(out->fifo, (void **)planes, count);
        if (err >= 0 && err != count) err = AVERROR(ENOMEM);
    }
    av_freep(&planes[0]);
    av_freep(&planes);
    return err < 0 ? err : count;
}

int cut_audio(CutOutput *out, const float *data, int samples) {
    static const float zeros[16384] = {0};
    while (samples > 0) {
        int block = FFMIN(samples, 16384 / out->source_channels);
        int err = resample_audio(out, data ? data : zeros, block);
        if (err < 0) return err;
        if ((err = write_audio(out, 0)) < 0) return err;
        if (data) data += (size_t)block * out->source_channels;
        samples -= block;
    }
    return 0;
}

static int init_gpu_scale(CutOutput *out, const AVFrame *source) {
#ifdef _WIN32
    const char *name = "scale_d3d11", *args = "width=%d:height=%d:format=nv12";
#else
    const char *name = "scale_vt", *args = "w=%d:h=%d";
#endif
    const AVFilter *scale = avfilter_get_by_name(name);
    if (!scale) return AVERROR_FILTER_NOT_FOUND;
    avfilter_graph_free(&out->gpu_scale);
    out->gpu_scale = avfilter_graph_alloc();
    if (!out->gpu_scale) return AVERROR(ENOMEM);
    out->gpu_scale->nb_threads = 1;
    out->gpu_input = avfilter_graph_alloc_filter(out->gpu_scale, avfilter_get_by_name("buffer"), "input");
    if (!out->gpu_input) return AVERROR(ENOMEM);
    AVBufferSrcParameters *params = av_buffersrc_parameters_alloc();
    if (!params) return AVERROR(ENOMEM);
    params->format = source->format;
    params->width = source->width;
    params->height = source->height;
    params->time_base = out->video->time_base;
    params->sample_aspect_ratio = source->sample_aspect_ratio;
    params->hw_frames_ctx = source->hw_frames_ctx;
    int err = av_buffersrc_parameters_set(out->gpu_input, params);
    av_free(params);
    if (err < 0) return err;
    if ((err = avfilter_init_str(out->gpu_input, NULL)) < 0) return err;
    char opts[128];
    snprintf(opts, sizeof(opts), args, out->video->width, out->video->height);
    AVFilterContext *resize;
    err = avfilter_graph_create_filter(&resize, scale, "resize", opts, NULL, out->gpu_scale);
    if (err < 0) return err;
    err = avfilter_graph_create_filter(&out->gpu_output, avfilter_get_by_name("buffersink"), "output",
                                      NULL, NULL, out->gpu_scale);
    if (err < 0) return err;
    if ((err = avfilter_link(out->gpu_input, 0, resize, 0)) < 0 ||
        (err = avfilter_link(resize, 0, out->gpu_output, 0)) < 0)
        return err;
    err = avfilter_graph_config(out->gpu_scale, NULL);
    if (err >= 0) {
        out->gpu_frames = source->hw_frames_ctx->data;
        out->gpu_width = source->width;
        out->gpu_height = source->height;
    }
    return err;
}

int cut_video(CutOutput *out, CutInput *in, int64_t pts, int64_t duration, int *video_error) {
    *video_error = CUT_VIDEO_ENCODE_ERROR;
    AVFrame *source = in->last_video, *dest = out->picture;
    const AVCodecContext *v = out->video;
    AVHWFramesContext *hardware = source->hw_frames_ctx ? (AVHWFramesContext *)source->hw_frames_ctx->data : NULL;
    int err;
    enum AVColorTransferCharacteristic transfer = source->color_trc == AVCOL_TRC_UNSPECIFIED ?
        out->source_transfer : source->color_trc;
    int hdr = is_hdr(transfer);
    if (v->pix_fmt != AV_PIX_FMT_NV12) {
        if (hdr || !hardware || source->format != v->pix_fmt)
            return AVERROR(ENOSYS);
        av_frame_unref(dest);
        if (hardware->sw_format == AV_PIX_FMT_NV12 && source->width == v->width && source->height == v->height) {
            if ((err = av_frame_ref(dest, source)) < 0) return err;
        } else {
#ifdef __APPLE__
            // scale_vt preserves the input pixel format and bit depth.
            if (hardware->sw_format != AV_PIX_FMT_NV12) return AVERROR(ENOSYS);
#endif
            if (out->gpu_frames != hardware || out->gpu_width != source->width || out->gpu_height != source->height) {
                if ((err = init_gpu_scale(out, source)) < 0) return err;
            }
            if ((err = av_buffersrc_add_frame_flags(out->gpu_input, source, AV_BUFFERSRC_FLAG_KEEP_REF)) < 0 ||
                (err = av_buffersink_get_frame(out->gpu_output, dest)) < 0)
                return err;
        }
    } else {
        if (hardware) {
            if (!out->download) out->download = av_frame_alloc();
            if (!out->download) return AVERROR(ENOMEM);
            av_frame_unref(out->download);
            int64_t started = out->profile_video ? av_gettime_relative() : 0;
            err = av_hwframe_transfer_data(out->download, source, 0);
            if (out->profile_video) out->readback_us += av_gettime_relative() - started;
            if (err < 0) return err;
            if ((err = av_frame_copy_props(out->download, source)) < 0) return err;
            source = out->download;
        }
        if ((err = av_frame_make_writable(dest)) < 0) return err;
        if (hdr) {
            if (!out->tone_map) {
                out->tone_map = sws_alloc_context();
                if (!out->tone_map) return AVERROR(ENOMEM);
                out->tone_map->flags = SWS_BILINEAR;
                out->tone_map->intent = SWS_INTENT_PERCEPTUAL;
                if ((int64_t)v->width * v->height >= 1920 * 1080)
                    out->tone_map->threads = av_clip(av_cpu_count(), 1, 8);
            }
            AVFrame input = *source;
            input.color_trc = transfer;
            if (input.color_primaries == AVCOL_PRI_UNSPECIFIED) input.color_primaries = AVCOL_PRI_BT2020;
            if (input.colorspace == AVCOL_SPC_UNSPECIFIED) input.colorspace = AVCOL_SPC_BT2020_NCL;
            if (input.color_range == AVCOL_RANGE_UNSPECIFIED) input.color_range = AVCOL_RANGE_MPEG;
            dest->color_primaries = AVCOL_PRI_BT709;
            dest->color_trc = AVCOL_TRC_BT709;
            dest->colorspace = AVCOL_SPC_BT709;
            dest->color_range = AVCOL_RANGE_MPEG;
            int64_t started = out->profile_video ? av_gettime_relative() : 0;
            err = sws_scale_frame(out->tone_map, dest, &input);
            if (out->profile_video) out->tone_map_us += av_gettime_relative() - started;
        } else {
            out->scale = sws_getCachedContext(out->scale, source->width, source->height, source->format,
                                             dest->width, dest->height, dest->format, SWS_BILINEAR, NULL, NULL, NULL);
            if (!out->scale) return AVERROR(EINVAL);
            const int *input_matrix = sws_getCoefficients(source->colorspace == AVCOL_SPC_UNSPECIFIED ?
                                                        v->colorspace : source->colorspace);
            const int *output_matrix = sws_getCoefficients(v->colorspace);
            int full = source->color_range == AVCOL_RANGE_JPEG ||
                (source->color_range == AVCOL_RANGE_UNSPECIFIED && v->color_range == AVCOL_RANGE_JPEG);
            int output_full = v->color_range == AVCOL_RANGE_JPEG;
            err = sws_setColorspaceDetails(out->scale, input_matrix, full, output_matrix, output_full,
                                           0, 1 << 16, 1 << 16);
            if (err < 0) return err;
            err = sws_scale(out->scale, (const uint8_t * const *)source->data, source->linesize,
                            0, source->height, dest->data, dest->linesize);
        }
        if (err < 0) return err;
    }
    if (out->video_pts == AV_NOPTS_VALUE)
        av_log(out->video, AV_LOG_VERBOSE, "SilenceCut export frames: decoder=%s encoder=%s transfer=%s\n",
               hardware ? "hardware" : "software", in->mode >= CUT_HARDWARE_ENCODER ? "hardware" : "software",
               v->pix_fmt == AV_PIX_FMT_NV12 ? (hardware ? "readback" : "system-memory") :
               (out->gpu_scale ? "device-scale" : "device-direct"));
    dest->pts = av_rescale_q(pts, AV_TIME_BASE_Q, v->time_base);
    if (out->video_pts != AV_NOPTS_VALUE && dest->pts <= out->video_pts) dest->pts = out->video_pts + 1;
    out->video_pts = dest->pts;
    if (pts + duration == out->duration) out->video_final_pts = dest->pts;
    dest->duration = FFMAX(1, av_rescale_q(duration, AV_TIME_BASE_Q, v->time_base));
    return encode_video(out, dest, video_error);
}

int cut_finish(CutOutput *out, int *video_error) {
    int err;
    do {
        err = resample_audio(out, NULL, 0);
        if (err < 0) return err;
    } while (err > 0);
    if ((err = write_audio(out, 1)) < 0) return err;
    if (!out->audio_pts) return AVERROR_INVALIDDATA;
    if (out->video) {
        if (out->video_pts == AV_NOPTS_VALUE) return AVERROR_INVALIDDATA;
        if ((err = encode_video(out, NULL, video_error)) < 0) return err;
        if (out->profile_video)
            av_log(out->video, AV_LOG_VERBOSE, "SilenceCut video timing: readback=%.3fs tone-map=%.3fs encode-and-write=%.3fs\n",
                   out->readback_us / 1e6, out->tone_map_us / 1e6, out->video_encode_us / 1e6);
    }
    *video_error = 0;
    if ((err = encode(out, out->audio, out->audio_stream, NULL, NULL)) < 0) return err;
    if (out->tone_map) {
        // Some decoders expose HDR signaling only after decoding the first picture.
        AVCodecParameters *par = out->video_stream->codecpar;
        par->color_primaries = AVCOL_PRI_BT709;
        par->color_trc = AVCOL_TRC_BT709;
        par->color_space = AVCOL_SPC_BT709;
        par->color_range = AVCOL_RANGE_MPEG;
    }
    err = av_write_trailer(out->format);
    int close = avio_closep(&out->format->pb);
    return err < 0 ? err : close;
}

void cut_discard(CutOutput *out) {
    if (!out) return;
    avfilter_graph_free(&out->gpu_scale);
    sws_freeContext(out->scale);
    sws_freeContext(out->tone_map);
    swr_free(&out->resample);
    av_audio_fifo_free(out->fifo);
    av_frame_free(&out->download);
    av_frame_free(&out->picture);
    av_packet_free(&out->packet);
    avcodec_free_context(&out->audio);
    avcodec_free_context(&out->video);
    if (out->format) avio_closep(&out->format->pb);
    avformat_free_context(out->format);
    av_free(out);
}
