#ifndef SILENCECUT_INPUT_H
#define SILENCECUT_INPUT_H

#include <libavcodec/avcodec.h>
#include <libavfilter/avfilter.h>
#include <libavformat/avformat.h>
#include <libswresample/swresample.h>

struct CutInput {
    AVFormatContext *format;
    AVCodecContext *decoder[2];
    AVPacket *packet;
    AVFrame *decoded, *audio, *last_video, *filtered;
    AVChannelLayout layout, source_layout;
    SwrContext *convert;
    AVFilterGraph *graph;
    AVFilterContext *filter_input, *filter_output;
    int audio_index, video_index, active, eof, drain, rate, source_format, source_rate;
    int mode;
    enum AVPixelFormat hardware_format;
    int64_t origin, next_pts[2];
};

#endif
